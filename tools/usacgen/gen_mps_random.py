#!/usr/bin/env python3
"""Random but syntactically valid MPS212 side information (test tool).

usage (from a directory that holds xin/<name>.raw, .meta and .pos):
    python3 -I gen_mps_random.py <libxaac source dir> <src> <dst> <seed> <highrate 0|1> <freqres 1..7> [slots]

Replaces the MPS212 payload of every access unit but the first of the libxaac
raw stream xin/<src> by random payloads that follow the Mps212Data syntax
(ISO/IEC 23003-3 5.2) with every parameter coding mode (PCM, 1-D and 2-D
Huffman, frequency and time pairs, coarse and fine quantisation, band strides),
and writes xin/<dst>.raw / .meta. xin/<src>.pos lists "au start end bits" per
access unit (bit range of the original payload, from the decoder). The
Huffman trees are read from libxaac's ixheaacd_mps_rom.c. The result is used
to compare decoders on side information the libxaac encoder never produces
(tests: usac_xaac_test.go, xaac_mps_rand).
"""
import sys, os, re, random, math, struct
import numpy as np
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from usacgen_common import strip_comments, find_struct_init

SRC = sys.argv[1]
rom = strip_comments(open(os.path.join(SRC, 'decoder/ixheaacd_mps_rom.c')).read())

def tree(vals, n):
    return [(vals[2*i], vals[2*i+1]) for i in range(n)]

cld = find_struct_init(rom, 'ixheaacd_huff_cld_nodes')
icc = find_struct_init(rom, 'ixheaacd_huff_icc_nodes')
p0 = find_struct_init(rom, 'ixheaacd_huff_part0_nodes')
lavt = tree(find_struct_init(rom, 'ixheaacd_huff_lav_idx_nodes'), 3)
CLD1 = [tree(cld[i*60:(i+1)*60], 30) for i in range(3)]
c2 = cld[180:]
CLD2 = {}
for dt in range(3):
    for pr in range(2):
        base = (dt*2+pr)*424
        t = c2[base:base+424]
        CLD2[(dt, pr)] = [tree(t[0:30], 15), tree(t[30:100], 35), tree(t[100:226], 63), tree(t[226:424], 99)]
ICC1 = [tree(icc[i*14:(i+1)*14], 7) for i in range(3)]
i2 = icc[42:]
ICC2 = {}
for dt in range(3):
    for pr in range(2):
        base = (dt*2+pr)*232
        t = i2[base:base+232]
        ICC2[(dt, pr)] = [tree(t[0:6], 3), tree(t[6:36], 15), tree(t[36:106], 35), tree(t[106:232], 63)]
P0 = {0: tree(p0[0:60], 30), 1: tree(p0[60:74], 7)}

class BW:
    def __init__(s): s.b = []
    def w(s, v, n):
        for i in range(n-1, -1, -1): s.b.append((v >> i) & 1)

def rand_leaf(bw, tr, rng):
    node = 0
    while True:
        bit = rng.randrange(2)
        bw.w(bit, 1)
        node = tr[node][bit]
        if node <= 0:
            return node

def pcm_bits(rng, bw, nval, levels):
    maxg = {3: 5, 7: 6, 11: 2, 13: 4, 19: 4, 25: 3, 51: 4}.get(levels, 1)
    ld = np.float32(np.log(np.float32(levels)) / np.log(np.float32(2)))
    # libxaac: (FLOAT32)(log((FLOAT32)n) / log(2.f)) computed in double then cast
    ld = np.float32(math.log(float(np.float32(levels))) / math.log(2.0))
    i = 0
    while i < nval:
        g = min(maxg, nval - i)
        n = int(math.ceil(float(np.float32(g) * ld)))
        bw.w(rng.getrandbits(n) if n else 0, n)
        i += g

def strides(fs, bands):
    ch = [1, 2, 5, 28][fs]
    return (bands - 1)//ch + 1

def huff1d(bw, rng, typ, dt, nval, p0flag):
    t1 = (CLD1 if typ == 0 else ICC1)[dt]
    start = 0
    if p0flag:
        rand_leaf(bw, P0[typ], rng); start = 1
    for i in range(start, nval):
        node = rand_leaf(bw, t1, rng)
        od = -(node + 1)
        if od != 0: bw.w(rng.randrange(2), 1)

def huff2d(bw, rng, typ, dt, pairing, nval, chfac, p0a, p0b):
    lidx = -(rand_leaf(bw, lavt, rng) + 1)
    lavbase = 3 if typ == 0 else 1
    lav = 2*lidx + lavbase
    if p0a: rand_leaf(bw, P0[typ], rng)
    if p0b: rand_leaf(bw, P0[typ], rng)
    tabs = (CLD2 if typ == 0 else ICC2)[(dt, pairing)][lidx]
    esc = 0
    for i in range(0, nval, chfac):
        node = rand_leaf(bw, tabs, rng)
        if node == 0:
            esc += 1
        else:
            v = -(node + 1); a = v >> 4; b = v & 15
            s = a + b; d = a - b
            if s > lav: a, b = -s + (2*lav+1), -d
            else: a, b = s, d
            if a + b != 0: bw.w(rng.randrange(2), 1)
            if a - b != 0: bw.w(rng.randrange(2), 1)
    if esc: pcm_bits(rng, bw, 2*esc, 2*lav+1)

def ecpair(bw, rng, typ, nb, pair, coarse, dtb):
    pcm = rng.random() < 0.12
    bw.w(int(pcm), 1)
    if pcm:
        levels = (15 if coarse else 31) if typ == 0 else (4 if coarse else 8)
        pcm_bits(rng, bw, (2 if pair else 1)*nb, levels)
        return
    dt = [0, 0]
    if pair or dtb:
        dt[0] = rng.randrange(2); bw.w(dt[0], 1)
    if pair and (dt[0] == 0 or dtb):
        dt[1] = rng.randrange(2); bw.w(dt[1], 1)
    scheme2d = rng.random() < 0.7
    bw.w(int(scheme2d), 1)
    timepair = False
    if scheme2d and pair:
        timepair = rng.random() < 0.4
        bw.w(int(timepair), 1)
    vecs = [0, 1] if pair else [0]
    if not scheme2d:
        for v in vecs: huff1d(bw, rng, typ, dt[v], nb, dt[v] == 0)
    elif not timepair:
        t1 = (CLD1 if typ == 0 else ICC1)
        for v in vecs:
            n = nb; p0 = None
            if dt[v] == 0: n -= 1
            rest = n % 2; n -= rest
            huff2d(bw, rng, typ, dt[v], 0, n, 2, dt[v] == 0 and v == 0, dt[v] == 0 and v == 1)
            if rest: huff1d(bw, rng, typ, dt[v], 1, False)
    else:
        n = nb
        p0 = dt[0] == 0 or dt[1] == 0
        if p0: n -= 1
        yy = 1 if (dt[0] == 1 or dt[1] == 1) else 0
        huff2d(bw, rng, typ, yy, 1, n, 1, p0, p0)
    if pair and (dt[0] == 1 or dt[1] == 1):
        if dt[0] == 1 and not dtb: pass
        elif dt[1] == 1: pass
        else: bw.w(rng.randrange(2), 1)

def ecdata(bw, rng, typ, nsets, bands, indep):
    modes = [rng.choice([3, 3, 3, 0, 1, 2]) for _ in range(nsets)]
    # mode 2 interpolates between neighbours: keep it valid (followed by a set with mode != 2 ... libxaac needs i2<nsets only with ec); avoid
    for i in range(nsets):
        if modes[i] == 2: modes[i] = 1
    for m in modes: bw.w(m, 2)
    setidx = 0; pair = 0
    for i in range(nsets):
        if modes[i] != 3: continue
        if pair:
            pair = 0; continue
        later = any(m == 3 for m in modes[i+1:])
        pair = int(later and rng.random() < 0.5)
        coarse = rng.randrange(2); fs = rng.choice([0, 0, 1, 2, 3])
        bw.w(pair, 1); bw.w(coarse, 1); bw.w(fs, 2)
        nb = strides(fs, bands)
        dtb = (not (indep and i == 0)) or setidx > 0
        ecpair(bw, rng, typ, nb, bool(pair), bool(coarse), dtb)
        setidx += pair + 1

def gen_payload(rng, highrate, bands, slots, indep_au):
    bw = BW()
    nsets = 1
    if highrate:
        ft = int(os.environ['FT']) if 'FT' in os.environ else rng.randrange(2)
        nsets = int(os.environ['NSETS']) if 'NSETS' in os.environ else rng.randrange(1, 9)
        bw.w(ft, 1); bw.w(nsets - 1, 3)
        if ft:
            pos = sorted(rng.sample(range(slots), nsets))
            nb = 5 if slots == 32 else 6
            for p in pos: bw.w(p, nb)
    if not indep_au:
        indep = rng.randrange(2); bw.w(indep, 1)
    else:
        indep = 1
    ecdata(bw, rng, 0, nsets, bands, bool(indep))
    ecdata(bw, rng, 1, nsets, bands, bool(indep))
    if highrate:
        for ps in range(nsets):
            sm = int(os.environ['SM']) if 'SM' in os.environ else rng.randrange(4); bw.w(sm, 2)
            if sm >= 2: bw.w(rng.randrange(4), 2)
            if sm == 3:
                fs = rng.randrange(4); bw.w(fs, 2)
                for _ in range(strides(fs, bands)): bw.w(rng.randrange(2), 1)
    return bw.b

def main():
    src, dst, seed, hr, fr = sys.argv[2], sys.argv[3], int(sys.argv[4]), int(sys.argv[5]), int(sys.argv[6])
    bands = {1: 28, 2: 20, 3: 14, 4: 10, 5: 7, 6: 5, 7: 4}[fr]
    slots = int(sys.argv[7]) if len(sys.argv) > 7 else 32
    rng = random.Random(seed)
    meta = open('xin/%s.meta' % src).read()
    init = int(re.search(r'-dec_info_init:(\d+)', meta).group(1))
    sizes = [int(x) for x in re.findall(r'-ia_mp4_stsz_size:(\d+)', meta)]
    raw = open('xin/%s.raw' % src, 'rb').read()
    pos = {}
    for ln in open('xin/%s.pos' % src):
        a, s, e, t = map(int, ln.split()); pos[a] = (s, e, t)
    out = [raw[:init]]; p = init; newsizes = []
    for i, sz in enumerate(sizes):
        au = raw[p:p+sz]; p += sz
        if i > 0 and i in pos:
            s, e, t = pos[i]
            bits = [(au[k >> 3] >> (7 - (k & 7))) & 1 for k in range(8*len(au))]
            indep_au = bits[0]
            payload = gen_payload(rng, hr, bands, slots, indep_au)
            nb = bits[:s] + payload + bits[e:]
            while len(nb) % 8: nb.append(0)
            au = bytes(sum(nb[k+j] << (7-j) for j in range(8)) for k in range(0, len(nb), 8))
        out.append(au); newsizes.append(len(au))
    open('xin/%s.raw' % dst, 'wb').write(b''.join(out))
    lines = [l for l in meta.split('\n') if l.strip() and not l.startswith('-ia_mp4_stsz_size')]
    lines += ['-ia_mp4_stsz_size:%d' % n for n in newsizes]
    open('xin/%s.meta' % dst, 'w').write('\n'.join(lines) + '\n')
main()
