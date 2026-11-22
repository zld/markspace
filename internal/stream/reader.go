// Package stream 以有界队列串联解析后的样本流：分块读取、
// RTP 时间戳适配与慢消费者背压/取消。
package stream

import "context"

// Chunk 为带原始偏移的输入分块。
type Chunk struct {
	Offset int64
	Data   []byte
}

// DefaultChunkSize 为默认分块大小。
const DefaultChunkSize = 64 * 1024

// ReadChunks 以固定块大小读取 buf 并送入 out；取消时及时停止并关闭 out。
// 内存占用由块大小决定，与输入总长无关。
func ReadChunks(ctx context.Context, buf []byte, size int, out chan<- Chunk) error {
	defer close(out)
	if size <= 0 {
		size = DefaultChunkSize
	}
	for off := int64(0); off < int64(len(buf)); {
		end := off + int64(size)
		if end > int64(len(buf)) {
			end = int64(len(buf))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- Chunk{Offset: off, Data: buf[off:end]}:
			off = end
		}
	}
	return nil
}
