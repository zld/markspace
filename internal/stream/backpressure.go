package stream

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// ErrClosed 表示队列已关闭。
var ErrClosed = errors.New("stream: queue closed")

// Queue 为有界队列：容量固定，慢消费者造成背压而不扩张内存。
// 峰值占用可观测，用于证明内存上限由队列容量决定。
type Queue[T any] struct {
	ch   chan T
	done chan struct{}
	once sync.Once
	peak atomic.Int64
}

// NewQueue 创建容量为 capacity 的有界队列（最小为 1）。
func NewQueue[T any](capacity int) *Queue[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &Queue[T]{ch: make(chan T, capacity), done: make(chan struct{})}
}

// Send 发送元素；队列满时阻塞（背压），取消或关闭时返回错误。
func (q *Queue[T]) Send(ctx context.Context, v T) error {
	select {
	case <-q.done:
		return ErrClosed
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-q.done:
		return ErrClosed
	case q.ch <- v:
		q.observe()
		return nil
	}
}

// Recv 接收元素；ok 为 false 表示队列关闭且耗尽或上下文取消。
func (q *Queue[T]) Recv(ctx context.Context) (v T, ok bool) {
	select {
	case <-ctx.Done():
		return v, false
	case <-q.done:
		// 关闭后仍排空已缓冲元素，保留已确认输出边界。
		for {
			select {
			case item, open := <-q.ch:
				return item, open
			default:
				return v, false
			}
		}
	case item, open := <-q.ch:
		return item, open
	}
}

// Close 关闭队列，之后的 Send 返回 ErrClosed；已缓冲元素仍可排空。
// 注意：不关闭底层 channel，以避免与进行中的 Send 竞态导致 panic。
func (q *Queue[T]) Close() {
	q.once.Do(func() {
		close(q.done)
	})
}

// Peak 返回观测到的最大在队元素数。
func (q *Queue[T]) Peak() int64 { return q.peak.Load() }

// Cap 返回队列容量。
func (q *Queue[T]) Cap() int64 { return int64(cap(q.ch)) }

func (q *Queue[T]) observe() {
	n := int64(len(q.ch))
	for {
		p := q.peak.Load()
		if n <= p || q.peak.CompareAndSwap(p, n) {
			return
		}
	}
}
