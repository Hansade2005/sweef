"""Extract numeric constant tables from the pdftotext rendering of the AV1
specification (spec.txt). pdftotext duplicates every token of the spec's
code blocks ("N" newline "N,," ...); this module undoes that, strips page
headers, and parses nested brace initialisers."""
import re, sys

SPEC = "/tmp/claude-0/-home-user-sweef/88880b6c-b9c0-5b8b-838a-110015e2aca6/scratchpad/av1/spec.txt"

_raw = None
def load():
    global _raw
    if _raw is None:
        t = open(SPEC, encoding="utf-8", errors="replace").read()
        t = re.sub(r"^.*Section: .*Page \d+ of \d+.*$", "", t, flags=re.M)
        t = re.sub(r"^.*AV1 Bitstream & Decoding Process Specification.*$", "", t, flags=re.M)
        _raw = t
    return _raw

def dedupe(s):
    s = re.sub(r"/\*.*?\*/", "", s, flags=re.S)
    s = re.sub(r"//[^\n]*", "", s)
    # negative numbers lose their sign in the duplicate
    s = re.sub(r"(\d+)\s*\1\*\*(\d+)\s*\2,,", r"\1*\2,", s)
    s = re.sub(r"-(\d+)\s*\1,,", r"-\1,", s)
    s = re.sub(r"(\d+)\s*\1,,", r"\1,", s)
    return s

def find_table(name, occurrence=0):
    t = load()
    pat = re.compile(r"^\s*" + re.escape(name) + r"\s*\[", re.M)
    ms = [m for m in pat.finditer(t) if re.match(r"[^;=\n]*(\n[^;=\n]*){0,3}=\s*\{", t[m.end():m.end()+160])]
    if not ms:
        raise KeyError(name)
    m = ms[occurrence]
    i = t.index("=", m.end())
    j = t.index("{", i)
    depth = 0
    k = j
    while True:
        c = t[k]
        if c == "{": depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0: break
        k += 1
    body = t[j:k+1]
    return body

def parse(body):
    from consts import C
    body = dedupe(body)
    body = re.sub(r"([A-Z0-9_])[ \t]*\n[ \t]*([A-Z])", r"\1,\n\2", body)
    toks = re.findall(r"\{|\}|[^{},]+|,", body)
    pos = 0
    def ev(txt):
        txt = " ".join(txt.split())
        return int(eval(txt, {"__builtins__": {}}, C))
    def rec():
        nonlocal pos
        assert toks[pos] == "{"
        pos += 1
        out = []
        while toks[pos] != "}":
            t = toks[pos]
            if t == "{":
                out.append(rec())
            elif t == ",":
                pos += 1
            elif not t.strip():
                pos += 1
            else:
                out.append(ev(t)); pos += 1
        pos += 1
        return out
    return rec()

def shape(a):
    if isinstance(a, list):
        if not a: return (0,)
        s = [shape(x) for x in a]
        if all(x == s[0] for x in s):
            return (len(a),) + s[0]
        return ("ragged", tuple(s))
    return ()

def flatten(a):
    if isinstance(a, list):
        for x in a: yield from flatten(x)
    else:
        yield a

def table(name, occurrence=0):
    return parse(find_table(name, occurrence))

if __name__ == "__main__":
    for n in sys.argv[1:]:
        a = table(n)
        print(n, shape(a))
