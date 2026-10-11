package glsl

import "math"

func opCopy(d, s, n int) op {
	if d == s {
		return func(f *Frame) {}
	}
	switch n {
	case 1:
		return func(f *Frame) { f.R[d] = f.R[s] }
	case 2:
		return func(f *Frame) { r := f.R; r[d], r[d+1] = r[s], r[s+1] }
	case 3:
		return func(f *Frame) { r := f.R; a, b, c := r[s], r[s+1], r[s+2]; r[d], r[d+1], r[d+2] = a, b, c }
	case 4:
		return func(f *Frame) {
			r := f.R
			a, b, c, e := r[s], r[s+1], r[s+2], r[s+3]
			r[d], r[d+1], r[d+2], r[d+3] = a, b, c, e
		}
	}
	return func(f *Frame) { copy(f.R[d:d+n], f.R[s:s+n]) }
}

// opFill replicates the single lane at s into n lanes at d.
func opFill(d, s, n int) op {
	return func(f *Frame) {
		v := f.R[s]
		for i := 0; i < n; i++ {
			f.R[d+i] = v
		}
	}
}

func fbin(code byte, d, a, b, n, sa, sb int) op {
	if n == 1 {
		switch code {
		case '+':
			return func(f *Frame) { r := f.R; r[d] = bf(fl(r[a]) + fl(r[b])) }
		case '-':
			return func(f *Frame) { r := f.R; r[d] = bf(fl(r[a]) - fl(r[b])) }
		case '*':
			return func(f *Frame) { r := f.R; r[d] = bf(fl(r[a]) * fl(r[b])) }
		case '/':
			return func(f *Frame) { r := f.R; r[d] = bf(fl(r[a]) / fl(r[b])) }
		}
	}
	switch code {
	case '+':
		return func(f *Frame) {
			r := f.R
			for i := 0; i < n; i++ {
				r[d+i] = bf(fl(r[a+i*sa]) + fl(r[b+i*sb]))
			}
		}
	case '-':
		return func(f *Frame) {
			r := f.R
			for i := 0; i < n; i++ {
				r[d+i] = bf(fl(r[a+i*sa]) - fl(r[b+i*sb]))
			}
		}
	case '*':
		if n == 4 && sa == 1 && sb == 1 {
			return func(f *Frame) {
				r := f.R
				x0, x1, x2, x3 := fl(r[a])*fl(r[b]), fl(r[a+1])*fl(r[b+1]), fl(r[a+2])*fl(r[b+2]), fl(r[a+3])*fl(r[b+3])
				r[d], r[d+1], r[d+2], r[d+3] = bf(x0), bf(x1), bf(x2), bf(x3)
			}
		}
		return func(f *Frame) {
			r := f.R
			for i := 0; i < n; i++ {
				r[d+i] = bf(fl(r[a+i*sa]) * fl(r[b+i*sb]))
			}
		}
	case '/':
		return func(f *Frame) {
			r := f.R
			for i := 0; i < n; i++ {
				r[d+i] = bf(fl(r[a+i*sa]) / fl(r[b+i*sb]))
			}
		}
	}
	panic("fbin " + string(code))
}

// intBinFn returns the lane function for an integer operator.
func intBinFn(code string, uns bool) func(a, b uint32) uint32 {
	if uns {
		switch code {
		case "+":
			return func(a, b uint32) uint32 { return a + b }
		case "-":
			return func(a, b uint32) uint32 { return a - b }
		case "*":
			return func(a, b uint32) uint32 { return a * b }
		case "/":
			return func(a, b uint32) uint32 {
				if b == 0 {
					return 0xFFFFFFFF
				}
				return a / b
			}
		case "%":
			return func(a, b uint32) uint32 {
				if b == 0 {
					return 0
				}
				return a % b
			}
		case "<<":
			return func(a, b uint32) uint32 { return a << (b & 31) }
		case ">>":
			return func(a, b uint32) uint32 { return a >> (b & 31) }
		}
	} else {
		switch code {
		case "+":
			return func(a, b uint32) uint32 { return a + b }
		case "-":
			return func(a, b uint32) uint32 { return a - b }
		case "*":
			return func(a, b uint32) uint32 { return uint32(int32(a) * int32(b)) }
		case "/":
			return func(a, b uint32) uint32 {
				if int32(b) == 0 {
					return 0
				}
				return uint32(int32(a) / int32(b))
			}
		case "%":
			return func(a, b uint32) uint32 {
				if int32(b) == 0 {
					return 0
				}
				r := int32(a) % int32(b)
				if r != 0 && (r < 0) != (int32(b) < 0) {
					r += int32(b)
				}
				return uint32(r)
			}
		case "<<":
			return func(a, b uint32) uint32 { return uint32(int32(a) << (b & 31)) }
		case ">>":
			return func(a, b uint32) uint32 { return uint32(int32(a) >> (b & 31)) }
		}
	}
	switch code {
	case "&":
		return func(a, b uint32) uint32 { return a & b }
	case "|":
		return func(a, b uint32) uint32 { return a | b }
	case "^":
		return func(a, b uint32) uint32 { return a ^ b }
	}
	panic("intBinFn " + code)
}

func ibin(code string, uns bool, d, a, b, n, sa, sb int) op {
	if n == 1 {
		switch code {
		case "+":
			return func(f *Frame) { r := f.R; r[d] = r[a] + r[b] }
		case "-":
			return func(f *Frame) { r := f.R; r[d] = r[a] - r[b] }
		case "*":
			return func(f *Frame) { r := f.R; r[d] = uint32(int32(r[a]) * int32(r[b])) }
		}
	}
	fn := intBinFn(code, uns)
	return func(f *Frame) {
		r := f.R
		for i := 0; i < n; i++ {
			r[d+i] = fn(r[a+i*sa], r[b+i*sb])
		}
	}
}

// cmpScalar compares two scalars: code one of < <= > >=.
func cmpScalar(code string, k Kind, d, a, b int) op {
	switch k {
	case KFloat:
		switch code {
		case "<":
			return func(f *Frame) { r := f.R; r[d] = b2u(fl(r[a]) < fl(r[b])) }
		case "<=":
			return func(f *Frame) { r := f.R; r[d] = b2u(fl(r[a]) <= fl(r[b])) }
		case ">":
			return func(f *Frame) { r := f.R; r[d] = b2u(fl(r[a]) > fl(r[b])) }
		case ">=":
			return func(f *Frame) { r := f.R; r[d] = b2u(fl(r[a]) >= fl(r[b])) }
		}
	case KInt:
		switch code {
		case "<":
			return func(f *Frame) { r := f.R; r[d] = b2u(int32(r[a]) < int32(r[b])) }
		case "<=":
			return func(f *Frame) { r := f.R; r[d] = b2u(int32(r[a]) <= int32(r[b])) }
		case ">":
			return func(f *Frame) { r := f.R; r[d] = b2u(int32(r[a]) > int32(r[b])) }
		case ">=":
			return func(f *Frame) { r := f.R; r[d] = b2u(int32(r[a]) >= int32(r[b])) }
		}
	default:
		switch code {
		case "<":
			return func(f *Frame) { r := f.R; r[d] = b2u(r[a] < r[b]) }
		case "<=":
			return func(f *Frame) { r := f.R; r[d] = b2u(r[a] <= r[b]) }
		case ">":
			return func(f *Frame) { r := f.R; r[d] = b2u(r[a] > r[b]) }
		case ">=":
			return func(f *Frame) { r := f.R; r[d] = b2u(r[a] >= r[b]) }
		}
	}
	panic("cmpScalar")
}

// opEq compares n lanes for equality; result is 1 lane.
func opEq(k Kind, d, a, b, n int, neg bool) op {
	if k == KFloat {
		return func(f *Frame) {
			r := f.R
			eq := true
			for i := 0; i < n; i++ {
				if fl(r[a+i]) != fl(r[b+i]) {
					eq = false
					break
				}
			}
			r[d] = b2u(eq != neg)
		}
	}
	if n == 1 {
		return func(f *Frame) { r := f.R; r[d] = b2u((r[a] == r[b]) != neg) }
	}
	return func(f *Frame) {
		r := f.R
		eq := true
		for i := 0; i < n; i++ {
			if r[a+i] != r[b+i] {
				eq = false
				break
			}
		}
		r[d] = b2u(eq != neg)
	}
}

// opConv converts n lanes between scalar kinds.
func opConv(d, s, n int, from, to Kind) op {
	if from == to {
		return opCopy(d, s, n)
	}
	var fn func(u uint32) uint32
	switch {
	case from == KFloat && to == KInt:
		fn = func(u uint32) uint32 { return uint32(int32(f2i(fl(u)))) }
	case from == KFloat && to == KUint:
		fn = func(u uint32) uint32 { return f2u(fl(u)) }
	case from == KFloat && to == KBool:
		fn = func(u uint32) uint32 { return b2u(fl(u) != 0) }
	case (from == KInt) && to == KFloat:
		fn = func(u uint32) uint32 { return bf(float32(int32(u))) }
	case from == KUint && to == KFloat:
		fn = func(u uint32) uint32 { return bf(float32(u)) }
	case from == KBool && to == KFloat:
		fn = func(u uint32) uint32 {
			if u != 0 {
				return bf(1)
			}
			return 0
		}
	case from == KInt && to == KUint, from == KUint && to == KInt:
		return opCopy(d, s, n)
	case (from == KInt || from == KUint) && to == KBool:
		fn = func(u uint32) uint32 { return b2u(u != 0) }
	case from == KBool && (to == KInt || to == KUint):
		fn = func(u uint32) uint32 { return b2u(u != 0) }
	default:
		panic("opConv")
	}
	if n == 1 {
		return func(f *Frame) { f.R[d] = fn(f.R[s]) }
	}
	return func(f *Frame) {
		r := f.R
		for i := 0; i < n; i++ {
			r[d+i] = fn(r[s+i])
		}
	}
}

func f2i(x float32) int32 {
	if x != x {
		return 0
	}
	if x >= 2147483647 {
		return math.MaxInt32
	}
	if x <= -2147483648 {
		return math.MinInt32
	}
	return int32(x)
}

func f2u(x float32) uint32 {
	if x != x || x <= 0 {
		return 0
	}
	if x >= 4294967295 {
		return math.MaxUint32
	}
	return uint32(x)
}

// matrix multiply: (cols x rows) A [aC,aR] * B [bC,bR]  (column-major lanes)
func opMatMul(d, a, b, aC, aR, bC int) op {
	// result is bC columns of aR rows; requires aC == bR
	return func(f *Frame) {
		r := f.R
		var tmp [16]float32
		for c := 0; c < bC; c++ {
			for row := 0; row < aR; row++ {
				var s float32
				for k := 0; k < aC; k++ {
					s += fl(r[a+k*aR+row]) * fl(r[b+c*aC+k])
				}
				tmp[c*aR+row] = s
			}
		}
		for i := 0; i < bC*aR; i++ {
			r[d+i] = bf(tmp[i])
		}
	}
}

// matrix * vector: M [cols,rows] * v[cols] -> vec[rows]
func opMatVec(d, m, v, cols, rows int) op {
	return func(f *Frame) {
		r := f.R
		var tmp [4]float32
		for row := 0; row < rows; row++ {
			var s float32
			for k := 0; k < cols; k++ {
				s += fl(r[m+k*rows+row]) * fl(r[v+k])
			}
			tmp[row] = s
		}
		for i := 0; i < rows; i++ {
			r[d+i] = bf(tmp[i])
		}
	}
}

// vector * matrix: v[rows] * M[cols,rows] -> vec[cols]
func opVecMat(d, v, m, cols, rows int) op {
	return func(f *Frame) {
		r := f.R
		var tmp [4]float32
		for c := 0; c < cols; c++ {
			var s float32
			for k := 0; k < rows; k++ {
				s += fl(r[v+k]) * fl(r[m+c*rows+k])
			}
			tmp[c] = s
		}
		for i := 0; i < cols; i++ {
			r[d+i] = bf(tmp[i])
		}
	}
}
