#!/usr/bin/env python3
"""Cross-check every leaf CDF row of the spec-derived default tables against
the data segment of libaom 3.8.2 (BSD-2-Clause). libaom stores each CDF as
AOM_ICDF(x) = 32768 - x, 16-bit little endian, followed by a zero counter."""
import sys, os, struct, re
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import spectables as S

LIB = sys.argv[1] if len(sys.argv) > 1 else "/usr/lib/x86_64-linux-gnu/libaom.so.3.8.2"
data = open(LIB, "rb").read()

def leafs(a):
    if isinstance(a[0], list):
        for x in a:
            yield from leafs(x)
    else:
        yield a

text = S.load()
names = sorted(set(re.findall(r"^\s*(Default_[A-Za-z0-9_]*Cdf)\s*\[", text, re.M)))
tot = found = 0
bad = []
for n in names:
    a = S.table(n)
    nf = nt = 0
    for row in leafs(a):
        nt += 1
        pat = struct.pack("<%dH" % (len(row) - 2), *[(32768 - v) & 0xffff for v in row[:-2]])  # values before the terminal 32768 and counter
        if data.find(pat) >= 0:
            nf += 1
    tot += nt; found += nf
    if nf != nt:
        bad.append((n, nf, nt))
print("rows found in libaom: %d / %d" % (found, tot))
for b in bad: print("  MISSING", b)
