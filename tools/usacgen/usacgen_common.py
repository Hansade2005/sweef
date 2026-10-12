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
    vals = _numbers(body)
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




def _balanced(src, i):
    depth = 0
    j = i
    while True:
        c = src[j]
        if c == '{':
            depth += 1
        elif c == '}':
            depth -= 1
            if depth == 0:
                return j
        j += 1


def _numbers(body):
    body = re.sub(r'\(\s*(?:U?WORD\d+|FLOAT\d+|int|float)\s*\)', ' ', body)
    toks = re.findall(r'[-+]?(?:0[xX][0-9a-fA-F]+|\d+\.\d*(?:[eE][-+]?\d+)?f?|\.\d+(?:[eE][-+]?\d+)?f?|\d+[eE][-+]?\d+f?|\d+)', body.replace('{', ' ').replace('}', ' '))
    vals = []
    for t in toks:
        if t.lower().lstrip('+-').startswith('0x'):
            vals.append(int(t, 16))
            continue
        t = t.rstrip('fF')
        if '.' in t or 'e' in t.lower():
            vals.append(float(t))
        else:
            vals.append(int(t))
    return vals


def find_after_comment(raw, marker, count=None):
    """Values of the brace block that follows the first occurrence of the
    comment text `marker` in the raw (comment carrying) source, for tables
    that sit in a large initializer and are only named by a comment."""
    k = raw.index(marker)
    i = raw.index('{', k)
    j = _balanced(raw, i)
    vals = _numbers(strip_comments(raw[i:j + 1]))
    if count is not None and len(vals) != count:
        raise SystemExit('%s: %d values, expected %d' % (marker, len(vals), count))
    return vals


def find_struct_init(src, name):
    """Values of `name = { ... };` without array dimensions (struct values)."""
    m = re.search(r'\b' + re.escape(name) + r'\s*=\s*\{', src)
    if not m:
        raise SystemExit('struct init %s not found' % name)
    i = m.end() - 1
    j = _balanced(src, i)
    return _numbers(src[i:j + 1])


def find_exprs(src, name):
    """Values of array `name` whose initialisers are simple constant
    expressions such as `1.0 / 256.0`."""
    m = re.search(r'\b' + re.escape(name) + r'((?:\s*\[[^\]]*\])+)\s*=\s*\{', src)
    if not m:
        raise SystemExit('array %s not found' % name)
    i = m.end() - 1
    j = _balanced(src, i)
    out = []
    for item in src[i + 1:j].split(','):
        item = item.strip()
        if not item:
            continue
        if not re.fullmatch(r'[0-9eE.+\-*/ ()f]+', item):
            raise SystemExit('%s: unsupported expression %r' % (name, item))
        out.append(float(eval(item.replace('f', ''))))
    return out
