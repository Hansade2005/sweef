package glsl

import (
	"fmt"
	"strings"
)

// Kind is the base kind of a GLSL type.
type Kind uint8

const (
	KVoid Kind = iota
	KBool
	KInt
	KUint
	KFloat
	KSampler
	KStruct
)

// SamplerKind describes a sampler type.
type SamplerKind uint8

const (
	Sampler2D SamplerKind = iota + 1
	SamplerCube
	Sampler3D
	Sampler2DArray
	Sampler2DShadow
	SamplerCubeShadow
	Sampler2DArrayShadow
	ISampler2D
	ISampler3D
	ISamplerCube
	ISampler2DArray
	USampler2D
	USampler3D
	USamplerCube
	USampler2DArray
)

// IsShadow reports whether k is a shadow sampler.
func (k SamplerKind) IsShadow() bool {
	return k == Sampler2DShadow || k == SamplerCubeShadow || k == Sampler2DArrayShadow
}

// ResultKind is the component kind a sampler returns.
func (k SamplerKind) ResultKind() Kind {
	switch k {
	case ISampler2D, ISampler3D, ISamplerCube, ISampler2DArray:
		return KInt
	case USampler2D, USampler3D, USamplerCube, USampler2DArray:
		return KUint
	}
	return KFloat
}

// Dim returns 2, 3 (3D), 6 (cube) or 22 (2D array).
func (k SamplerKind) Dim() int {
	switch k {
	case Sampler2D, Sampler2DShadow, ISampler2D, USampler2D:
		return 2
	case Sampler3D, ISampler3D, USampler3D:
		return 3
	case SamplerCube, SamplerCubeShadow, ISamplerCube, USamplerCube:
		return 6
	}
	return 22
}

// Type is a GLSL type. Arrays are Types with Arr > 0 whose element type is
// the same Type with Arr == 0.
type Type struct {
	K   Kind
	N   int // vector size or matrix rows (1 = scalar)
	M   int // matrix columns (1 = not a matrix)
	Arr int
	S   *StructT
	SK  SamplerKind
}

// Field is a struct member.
type Field struct {
	Name string
	T    Type
	Off  int // lane offset
}

// StructT is a struct definition.
type StructT struct {
	Name   string
	Fields []Field
	Size   int
}

var (
	tVoid  = Type{K: KVoid, N: 1, M: 1}
	tBool  = Type{K: KBool, N: 1, M: 1}
	tInt   = Type{K: KInt, N: 1, M: 1}
	tUint  = Type{K: KUint, N: 1, M: 1}
	tFloat = Type{K: KFloat, N: 1, M: 1}
)

func vecT(k Kind, n int) Type { return Type{K: k, N: n, M: 1} }
func matT(c, r int) Type      { return Type{K: KFloat, N: r, M: c} }

func (t Type) Elem() Type { t.Arr = 0; return t }

// BaseSize is the lane count of one element.
func (t Type) BaseSize() int {
	switch t.K {
	case KStruct:
		return t.S.Size
	case KVoid:
		return 0
	case KSampler:
		return 1
	}
	return t.N * t.M
}

// Size is the lane count of the whole value.
func (t Type) Size() int {
	if t.Arr > 0 {
		return t.BaseSize() * t.Arr
	}
	return t.BaseSize()
}

func (t Type) IsScalar() bool {
	return t.Arr == 0 && t.K != KStruct && t.K != KSampler && t.K != KVoid && t.N == 1 && t.M == 1
}
func (t Type) IsVec() bool {
	return t.Arr == 0 && t.K != KStruct && t.K != KSampler && t.K != KVoid && t.N > 1 && t.M == 1
}
func (t Type) IsMat() bool { return t.Arr == 0 && t.K == KFloat && t.M > 1 }
func (t Type) IsNumeric() bool {
	return t.K == KInt || t.K == KUint || t.K == KFloat
}

// ScalarOf returns the scalar type with the same kind.
func (t Type) ScalarOf() Type { return Type{K: t.K, N: 1, M: 1} }

func (t Type) String() string {
	var s string
	switch t.K {
	case KVoid:
		s = "void"
	case KStruct:
		s = t.S.Name
		if s == "" {
			s = "<anonymous struct>"
		}
	case KSampler:
		s = samplerName(t.SK)
	default:
		pre := map[Kind]string{KBool: "b", KInt: "i", KUint: "u", KFloat: ""}[t.K]
		base := map[Kind]string{KBool: "bool", KInt: "int", KUint: "uint", KFloat: "float"}[t.K]
		switch {
		case t.M > 1:
			if t.M == t.N {
				s = fmt.Sprintf("mat%d", t.M)
			} else {
				s = fmt.Sprintf("mat%dx%d", t.M, t.N)
			}
		case t.N > 1:
			if t.K == KFloat {
				s = fmt.Sprintf("vec%d", t.N)
			} else {
				s = fmt.Sprintf("%svec%d", pre, t.N)
			}
		default:
			s = base
		}
	}
	if t.Arr > 0 {
		s += fmt.Sprintf("[%d]", t.Arr)
	}
	return s
}

func samplerName(k SamplerKind) string {
	for n, v := range samplerTypes {
		if v == k {
			return n
		}
	}
	return "sampler"
}

var samplerTypes = map[string]SamplerKind{
	"sampler2D": Sampler2D, "samplerCube": SamplerCube, "sampler3D": Sampler3D, "sampler2DArray": Sampler2DArray,
	"sampler2DShadow": Sampler2DShadow, "samplerCubeShadow": SamplerCubeShadow, "sampler2DArrayShadow": Sampler2DArrayShadow,
	"isampler2D": ISampler2D, "isampler3D": ISampler3D, "isamplerCube": ISamplerCube, "isampler2DArray": ISampler2DArray,
	"usampler2D": USampler2D, "usampler3D": USampler3D, "usamplerCube": USamplerCube, "usampler2DArray": USampler2DArray,
}

// basicType maps a type keyword to its Type.
func basicType(name string) (Type, bool) {
	switch name {
	case "void":
		return tVoid, true
	case "bool":
		return tBool, true
	case "int":
		return tInt, true
	case "uint":
		return tUint, true
	case "float":
		return tFloat, true
	}
	if k, ok := samplerTypes[name]; ok {
		return Type{K: KSampler, N: 1, M: 1, SK: k}, true
	}
	if strings.HasPrefix(name, "mat") {
		switch name {
		case "mat2", "mat2x2":
			return matT(2, 2), true
		case "mat3", "mat3x3":
			return matT(3, 3), true
		case "mat4", "mat4x4":
			return matT(4, 4), true
		case "mat2x3":
			return matT(2, 3), true
		case "mat2x4":
			return matT(2, 4), true
		case "mat3x2":
			return matT(3, 2), true
		case "mat3x4":
			return matT(3, 4), true
		case "mat4x2":
			return matT(4, 2), true
		case "mat4x3":
			return matT(4, 3), true
		}
		return Type{}, false
	}
	for _, p := range []struct {
		pre string
		k   Kind
	}{{"vec", KFloat}, {"bvec", KBool}, {"ivec", KInt}, {"uvec", KUint}} {
		if strings.HasPrefix(name, p.pre) && len(name) == len(p.pre)+1 {
			n := int(name[len(name)-1] - '0')
			if n >= 2 && n <= 4 {
				return vecT(p.k, n), true
			}
		}
	}
	return Type{}, false
}
