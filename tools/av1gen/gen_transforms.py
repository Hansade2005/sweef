#!/usr/bin/env python3
"""Generate straight-line Go for the AV1 inverse DCT (4..64) and ADST (8, 16)
by executing the butterfly programs of spec sections 7.13.2.3-7.13.2.9
symbolically. Cosine constants come from Cos128_Lookup (spec 7.13.2.1)."""
import sys, os
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import spectables as S

OUT = sys.argv[1] if len(sys.argv) > 1 else "transform_gen.go"
COS = S.table("Cos128_Lookup")

def cos128(angle):
    a = angle & 255
    if a <= 64: return COS[a]
    if a <= 128: return -COS[128 - a]
    if a <= 192: return -COS[a - 128]
    return COS[256 - a]

def sin128(angle): return cos128(angle - 64)

def brev(nb, x):
    t = 0
    for i in range(nb):
        t += ((x >> i) & 1) << (nb - 1 - i)
    return t

class Gen:
    def __init__(self, name, n0, comment):
        self.name = name; self.n0 = n0
        self.lines = []
        self.names = ["t%d" % i for i in range(n0)]   # name currently held in T[i]
        self.tmp = 0
        self.neg = {}  # not used
        self.comment = comment
    def newvar(self):
        self.tmp += 1
        return "v%d" % self.tmp
    def B(self, a, b, angle, flip):
        ta, tb = self.names[a], self.names[b]
        c, s = cos128(angle), sin128(angle)
        x, y = self.newvar(), self.newvar()
        def term(v, k):
            return "int64(%s)*%d" % (v, k)
        # 64-bit products: with 12-bit video the row transform inputs span
        # 20 bits and a butterfly sum exceeds 32 bits.
        self.lines.append("%s := int32((%s - %s + 2048) >> 12)" % (x, term(ta, c), term(tb, s)))
        self.lines.append("%s := int32((%s + %s + 2048) >> 12)" % (y, term(ta, s), term(tb, c)))
        if flip:
            self.names[a], self.names[b] = y, x
        else:
            self.names[a], self.names[b] = x, y
    def H(self, a, b, flip):
        if flip: a, b = b, a
        ta, tb = self.names[a], self.names[b]
        x, y = self.newvar(), self.newvar()
        self.lines.append("%s := clip(%s+%s, lo, hi)" % (x, ta, tb))
        self.lines.append("%s := clip(%s-%s, lo, hi)" % (y, ta, tb))
        self.names[a], self.names[b] = x, y
    def emit(self):
        n0 = self.n0
        out = ["// %s\nfunc %s(t []int32, lo, hi int32) {\n" % (self.comment, self.name)]
        out.append("\t_ = t[%d]\n" % (n0 - 1))
        # loads: names at start are t0..tn; we declare them from t[]
        for i in range(n0):
            pass
        return out

def dct(n):
    n0 = 1 << n
    g = Gen("idct%d" % n0, n0, "idct%d is the inverse DCT of length %d (spec 7.13.2.3); in-place on t." % (n0, n0))
    # permutation: T[i] = copyT[brev(n,i)]
    g.names = ["t%d" % brev(n, i) for i in range(n0)]
    B, H = g.B, g.H
    if n == 6:
        for i in range(16): B(32 + i, 63 - i, 63 - 4 * brev(4, i), 0)
    if n >= 5:
        for i in range(8): B(16 + i, 31 - i, 6 + (brev(3, 7 - i) << 3), 0)
    if n == 6:
        for i in range(16): H(32 + i * 2, 33 + i * 2, i & 1)
    if n >= 4:
        for i in range(4): B(8 + i, 15 - i, 12 + (brev(2, 3 - i) << 4), 0)
    if n >= 5:
        for i in range(8): H(16 + 2 * i, 17 + 2 * i, i & 1)
    if n == 6:
        for i in range(4):
            for j in range(2): B(62 - i * 4 - j, 33 + i * 4 + j, 60 - 16 * brev(2, i) + 64 * j, 1)
    if n >= 3:
        for i in range(2): B(4 + i, 7 - i, 56 - 32 * i, 0)
    if n >= 4:
        for i in range(4): H(8 + 2 * i, 9 + 2 * i, i & 1)
    if n >= 5:
        for i in range(2):
            for j in range(2): B(30 - 4 * i - j, 17 + 4 * i + j, 24 + (j << 6) + ((1 - i) << 5), 1)
    if n == 6:
        for i in range(8):
            for j in range(2): H(32 + i * 4 + j, 35 + i * 4 - j, i & 1)
    for i in range(2): B(2 * i, 2 * i + 1, 32 + 16 * i, 1 - i)
    if n >= 3:
        for i in range(2): H(4 + 2 * i, 5 + 2 * i, i)
    if n >= 4:
        for i in range(2): B(14 - i, 9 + i, 48 + 64 * i, 1)
    if n >= 5:
        for i in range(4):
            for j in range(2): H(16 + 4 * i + j, 19 + 4 * i - j, i & 1)
    if n == 6:
        for i in range(2):
            for j in range(4): B(61 - i * 8 - j, 34 + i * 8 + j, 56 - i * 32 + (j >> 1) * 64, 1)
    for i in range(2): H(i, 3 - i, 0)
    if n >= 3: B(6, 5, 32, 1)
    if n >= 4:
        for i in range(2):
            for j in range(2): H(8 + 4 * i + j, 11 + 4 * i - j, i)
    if n >= 5:
        for i in range(4): B(29 - i, 18 + i, 48 + (i >> 1) * 64, 1)
    if n == 6:
        for i in range(4):
            for j in range(4): H(32 + 8 * i + j, 39 + 8 * i - j, i & 1)
    if n >= 3:
        for i in range(4): H(i, 7 - i, 0)
    if n >= 4:
        for i in range(2): B(13 - i, 10 + i, 32, 1)
    if n >= 5:
        for i in range(2):
            for j in range(4): H(16 + i * 8 + j, 23 + i * 8 - j, i)
    if n == 6:
        for i in range(8): B(59 - i, 36 + i, 48 if i < 4 else 112, 1)
    if n >= 4:
        for i in range(8): H(i, 15 - i, 0)
    if n >= 5:
        for i in range(4): B(27 - i, 20 + i, 32, 1)
    if n == 6:
        for i in range(8):
            H(32 + i, 47 - i, 0)
            H(48 + i, 63 - i, 1)
    if n >= 5:
        for i in range(16): H(i, 31 - i, 0)
    if n == 6:
        for i in range(8): B(55 - i, 40 + i, 32, 1)
    if n == 6:
        for i in range(32): H(i, 63 - i, 0)
    return g

def adst_in_perm(g, n):
    n0 = 1 << n
    old = list(g.names)
    for i in range(n0):
        idx = (i - 1) if (i & 1) else (n0 - i - 1)
        g.names[i] = old[idx]

def adst_out_perm(g, n):
    n0 = 1 << n
    old = list(g.names)
    # T[i] = (i&1) ? -copyT[idx] : copyT[idx]; negation emitted as code
    for i in range(n0):
        a = (i >> 3) & 1
        b = ((i >> 2) & 1) ^ ((i >> 3) & 1)
        c = ((i >> 1) & 1) ^ ((i >> 2) & 1)
        d = (i & 1) ^ ((i >> 1) & 1)
        idx = ((d << 3) | (c << 2) | (b << 1) | a) >> (4 - n)
        if i & 1:
            v = g.newvar()
            g.lines.append("%s := -%s" % (v, old[idx]))
            g.names[i] = v
        else:
            g.names[i] = old[idx]

def adst8():
    g = Gen("iadst8", 8, "iadst8 is the inverse ADST of length 8 (spec 7.13.2.7).")
    adst_in_perm(g, 3)
    B, H = g.B, g.H
    for i in range(4): B(2 * i, 2 * i + 1, 60 - 16 * i, 1)
    for i in range(4): H(i, 4 + i, 0)
    for i in range(2): B(4 + 3 * i, 5 + i, 48 - 32 * i, 1)
    for i in range(2):
        for j in range(2): H(4 * j + i, 2 + 4 * j + i, 0)
    for i in range(2): B(2 + 4 * i, 3 + 4 * i, 32, 1)
    adst_out_perm(g, 3)
    return g

def adst16():
    g = Gen("iadst16", 16, "iadst16 is the inverse ADST of length 16 (spec 7.13.2.8).")
    adst_in_perm(g, 4)
    B, H = g.B, g.H
    for i in range(8): B(2 * i, 2 * i + 1, 62 - 8 * i, 1)
    for i in range(8): H(i, 8 + i, 0)
    for i in range(2):
        B(8 + 2 * i, 9 + 2 * i, 56 - 32 * i, 1)
        B(13 + 2 * i, 12 + 2 * i, 8 + 32 * i, 1)
    for i in range(4):
        for j in range(2): H(8 * j + i, 4 + 8 * j + i, 0)
    for i in range(2):
        for j in range(2): B(4 + 8 * j + 3 * i, 5 + 8 * j + i, 48 - 32 * i, 1)
    for i in range(2):
        for j in range(4): H(4 * j + i, 2 + 4 * j + i, 0)
    for i in range(4): B(2 + 4 * i, 3 + 4 * i, 32, 1)
    adst_out_perm(g, 4)
    return g

def render(g):
    n0 = g.n0
    out = ["// %s\nfunc %s(t []int32, lo, hi int32) {\n" % (g.comment, g.name)]
    out.append("\t_ = t[%d]\n" % (n0 - 1))
    # load only variables actually referenced
    body = "\n".join(g.lines)
    used = set()
    import re
    for m in re.finditer(r"\bt(\d+)\b", body + " " + " ".join(g.names)):
        used.add(int(m.group(1)))
    for i in sorted(used):
        out.append("\tt%d := t[%d]\n" % (i, i))
    for l in g.lines:
        out.append("\t" + l + "\n")
    for i in range(n0):
        out.append("\tt[%d] = %s\n" % (i, g.names[i]))
    out.append("}\n\n")
    return "".join(out)

hdr = """// Code generated by gen_transforms.py from the butterfly programs of spec
// sections 7.13.2.3 - 7.13.2.8. DO NOT EDIT.

package av1

func clip(x, lo, hi int32) int32 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

"""
with open(OUT, "w") as f:
    f.write(hdr)
    for n in range(2, 7):
        f.write(render(dct(n)))
    f.write(render(adst8()))
    f.write(render(adst16()))
print("wrote", OUT)
