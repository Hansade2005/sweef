"""Shared C table extraction helpers of the USAC generators."""
import re

def strip_comments(s):
    s = re.sub(r'/\*.*?\*/', '', s, flags=re.S)
    s = re.sub(r'//[^\n]*', '', s)
    return s


def find_array(src, name):
    """Return (dims, flat_values) of array `name`."""
    m = re.search(r'\b' + re.escape(name) + r'((?:\s*\[[^\]]*\])+)\s*=\s*\{', src)
    if not m:
        raise SystemExit('array %s not found' % name)
    dims_txt = re.findall(r'\[([^\]]*)\]', m.group(1))
    # body: balanced braces
    i = m.end() - 1
    depth = 0
    j = i
    while True:
        c = src[j]
        if c == '{':
            depth += 1
        elif c == '}':
            depth -= 1
            if depth == 0:
                break
        j += 1
    body = src[i:j + 1]
    toks = re.findall(r'[-+]?(?:0[xX][0-9a-fA-F]+|\d+\.\d*(?:[eE][-+]?\d+)?f?|\d+[eE][-+]?\d+f?|\d+)', body.replace('{', ' ').replace('}', ' '))
    vals = []
    for t in toks:
        t = t.rstrip('f')
        if t.lower().startswith(('0x', '-0x', '+0x')):
            vals.append(int(t, 16))
        elif '.' in t or 'e' in t.lower():
            vals.append(float(t))
        else:
            vals.append(int(t))
    # compare the element count with the declared dimensions when they are numeric
    if dims_txt and all(re.fullmatch(r'\d+', x.strip()) for x in dims_txt if x.strip()) and all(x.strip() for x in dims_txt):
        n = 1
        for x in dims_txt:
            n *= int(x)
        if n != len(vals):
            raise SystemExit('array %s: %d elements, declared %d' % (name, len(vals), n))
    return dims_txt, vals


RANGES = {
    'uint8': (0, 255), 'int8': (-128, 127), 'uint16': (0, 65535), 'int16': (-32768, 32767),
    'uint32': (0, 2**32 - 1), 'int32': (-2**31, 2**31 - 1), 'int': (-2**31, 2**31 - 1),
}


def emit(f, goname, gotype, dims, vals, doc, per_line=12):
    isf = gotype.startswith('float')
    if not isf:
        lo, hi = RANGES[gotype]
        for v in vals:
            assert lo <= v <= hi, (goname, v)
    f.write('\n// %s\n' % doc)
    f.write('var %s = [...]%s{\n' % (goname, gotype))
    for k in range(0, len(vals), per_line):
        chunk = vals[k:k + per_line]
        if isf:
            f.write('\t' + ', '.join(repr(float(v)) for v in chunk) + ',\n')
        elif gotype.startswith('uint') and max(chunk) > 255:
            f.write('\t' + ', '.join(str(v) for v in chunk) + ',\n')
        else:
            f.write('\t' + ', '.join(str(v) for v in chunk) + ',\n')
    f.write('}\n')


