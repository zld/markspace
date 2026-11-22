// Package timeline 提供统一有理数时间基准、时间线合成与冲突元数据裁决。
//
// 所有子系统共享本包的有理数时间基准（Rat/Time）与阶段错误模型（Error），
// 保证容器解析、流处理与证据输出在同一时间语义下可互相核对。
package timeline

import "fmt"

// MasterScale 是全流程统一的主时基（每秒刻度数），与 RTP 90kHz 时钟一致。
const MasterScale int64 = 90000

// Rat 为最简有理数，分母恒为正；零值表示 0/1。
type Rat struct {
	Num int64
	Den int64
}

func gcd(a, b int64) int64 {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

// NewRat 归一化分子分母；den 为 0 时按零值处理。
func NewRat(num, den int64) Rat {
	if den == 0 {
		return Rat{}
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(num, den)
	return Rat{Num: num / g, Den: den / g}
}

// norm 将零值分母归一为 1，使零值 Rat 可安全参与运算。
func (r Rat) norm() Rat {
	if r.Den == 0 {
		return Rat{Num: r.Num, Den: 1}
	}
	return r
}

// Add 返回两有理数之和。
func (r Rat) Add(o Rat) Rat {
	r, o = r.norm(), o.norm()
	return NewRat(r.Num*o.Den+o.Num*r.Den, r.Den*o.Den)
}

// Sub 返回两有理数之差。
func (r Rat) Sub(o Rat) Rat {
	r, o = r.norm(), o.norm()
	return NewRat(r.Num*o.Den-o.Num*r.Den, r.Den*o.Den)
}

// Mul 返回两有理数之积。
func (r Rat) Mul(o Rat) Rat {
	r, o = r.norm(), o.norm()
	return NewRat(r.Num*o.Num, r.Den*o.Den)
}

// Div 返回两有理数之商；除数为零时返回零值。
func (r Rat) Div(o Rat) Rat {
	r, o = r.norm(), o.norm()
	if o.Num == 0 {
		return Rat{}
	}
	return NewRat(r.Num*o.Den, r.Den*o.Num)
}

// Cmp 比较大小：-1 小于，0 相等，1 大于。
func (r Rat) Cmp(o Rat) int {
	r, o = r.norm(), o.norm()
	l, rr := r.Num*o.Den, o.Num*r.Den
	switch {
	case l < rr:
		return -1
	case l > rr:
		return 1
	default:
		return 0
	}
}

// Float64 返回浮点近似值，仅用于统计与展示。
func (r Rat) Float64() float64 {
	r = r.norm()
	return float64(r.Num) / float64(r.Den)
}

// Milli 返回毫秒浮点值，仅用于展示与误差统计。
func (r Rat) Milli() float64 { return r.Float64() * 1000 }

// Abs 返回绝对值。
func (r Rat) Abs() Rat {
	if r.Num < 0 {
		return Rat{Num: -r.Num, Den: r.Den}
	}
	return r
}

// Time 表示某时基下的整数刻度时间，值 = Units/Scale 秒。
type Time struct {
	Units int64
	Scale int64
}

// Rat 转换为有理数秒。
func (t Time) Rat() Rat { return NewRat(t.Units, t.Scale) }

// ToScale 换算到目标时基，采用确定性舍入。
func (t Time) ToScale(scale int64) Time {
	return Time{Units: Convert(t.Units, t.Scale, scale), Scale: scale}
}

func (t Time) String() string { return fmt.Sprintf("%d/%d", t.Units, t.Scale) }

// Convert 将 units 从 fromScale 换算到 toScale。
// 舍入规则为"半数远离零"，对单调输入保持单调，且结果完全确定。
func Convert(units, fromScale, toScale int64) int64 {
	if fromScale <= 0 || toScale <= 0 {
		return 0
	}
	if units < 0 {
		return -Convert(-units, fromScale, toScale)
	}
	n := units * toScale
	q, rem := n/fromScale, n%fromScale
	if rem*2 >= fromScale {
		q++
	}
	return q
}
