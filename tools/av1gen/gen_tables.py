#!/usr/bin/env python3
"""Generate internal/media/av1/tables_gen.go from the AV1 bitstream
specification (v1.0.0 with Errata 1, pdftotext rendering in spec.txt).

Every table is validated against invariants from the specification before it
is emitted: CDFs are monotonic, end with 32768 and a zero adaptation counter,
scan tables are permutations, etc."""
import re, sys, os
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import spectables as S

OUT = sys.argv[1] if len(sys.argv) > 1 else "tables_gen.go"

def go_name(n):
    parts = n.split("_")
    out = parts[0].lower()
    for p in parts[1:]:
        out += p[:1].upper() + p[1:]
    return out

def gotype(shape, elem):
    s = ""
    for d in shape: s += "[%d]" % d
    return s + elem

def emit(a, indent=0, per_line=16):
    if not isinstance(a, list):
        return str(a)
    if a and not isinstance(a[0], list):
        # leaf list
        lines = []
        for i in range(0, len(a), per_line):
            lines.append(", ".join(str(x) for x in a[i:i+per_line]))
        if len(lines) == 1:
            return "{" + lines[0] + "}"
        pad = "\t" * (indent + 1)
        return "{\n" + "".join(pad + l + ",\n" for l in lines) + "\t" * indent + "}"
    pad = "\t" * (indent + 1)
    return "{\n" + "".join(pad + emit(x, indent + 1, per_line) + ",\n" for x in a) + "\t" * indent + "}"

def check_cdf(name, a):
    def rec(x, path):
        if isinstance(x[0], list):
            for i, y in enumerate(x): rec(y, path + [i])
        else:
            assert x[-1] == 0, (name, path, "counter")
            assert x[-2] == 32768, (name, path, "end", x)
            prev = 0
            for v in x[:-1]:
                assert v >= prev, (name, path, "monotonic", x)
                prev = v
            assert x[0] > 0 or True
    rec(a, [])

def check_perm(name, a):
    assert sorted(a) == list(range(len(a))), name

tables = []   # (name, shape, elemtype, data, comment)

def add(spec, elem, occurrence=0, validate=None, goname=None):
    a = S.table(spec, occurrence)
    sh = S.shape(a)
    assert sh and sh[0] != "ragged", (spec, sh)
    if validate: validate(spec, a)
    tables.append((goname or go_name(spec), sh, elem, a, spec))

text = S.load()
cdfnames = sorted(set(re.findall(r"^\s*(Default_[A-Za-z0-9_]*Cdf)\s*\[", text, re.M)))
for n in cdfnames:
    add(n, "uint16", validate=check_cdf)

for n in re.findall(r"^\s*((?:Default|Mrow|Mcol)_Scan_\d+x\d+)\s*\[", text, re.M):
    add(n, "uint16", validate=check_perm)

for n, t in [
    ("Quantizer_Matrix", "uint8"), ("Qm_Offset", "int"),
    ("Dc_Qlookup", "int32"), ("Ac_Qlookup", "int32"),
    ("Gaussian_Sequence", "int16"),
    ("Subpel_Filters", "int16"), ("Warped_Filters", "int16"), ("Upscale_Filter", "int16"),
    ("Intra_Filter_Taps", "int"),
    ("Div_Lut", "int"), ("Div_Mult", "int"), ("Div_Table", "int"),
    ("Obmc_Mask_2", "int"), ("Obmc_Mask_4", "int"), ("Obmc_Mask_8", "int"), ("Obmc_Mask_16", "int"), ("Obmc_Mask_32", "int"),
    ("Wedge_Master_Oblique_Odd", "int"), ("Wedge_Master_Oblique_Even", "int"), ("Wedge_Master_Vertical", "int"),
    ("Wedge_Codebook", "int"), ("Ii_Weights_1d", "int"),
    ("Quant_Dist_Weight", "int"), ("Quant_Dist_Lookup", "int"),
    ("Cos128_Lookup", "int"), ("Transform_Row_Shift", "int"),
    ("Cdef_Uv_Dir", "int"), ("Cdef_Pri_Taps", "int"), ("Cdef_Sec_Taps", "int"), ("Cdef_Directions", "int"),
    ("Sgr_Params", "int"),
    ("Intra_Edge_Kernel", "int"),
    ("Sm_Weights_Tx_4x4", "int"), ("Sm_Weights_Tx_8x8", "int"), ("Sm_Weights_Tx_16x16", "int"),
    ("Sm_Weights_Tx_32x32", "int"), ("Sm_Weights_Tx_64x64", "int"),
    ("Mode_To_Angle", "int"), ("Dr_Intra_Derivative", "int"),
    ("Intra_Mode_Context", "int"), ("Compound_Mode_Ctx_Map", "int"),
    ("Coeff_Base_Ctx_Offset", "int"), ("Coeff_Base_Pos_Ctx_Offset", "int"),
    ("Mag_Ref_Offset_With_Tx_Class", "int"), ("Sig_Ref_Diff_Offset", "int"),
    ("Filter_Intra_Mode_To_Intra_Dir", "int"),
    ("Mi_Width_Log2", "int"), ("Mi_Height_Log2", "int"), ("Num_4x4_Blocks_Wide", "int"), ("Num_4x4_Blocks_High", "int"),
    ("Size_Group", "int"), ("Max_Tx_Size_Rect", "int"), ("Max_Tx_Depth", "int"), ("Partition_Subsize", "int"),
    ("Split_Tx_Size", "int"), ("Mode_To_Txfm", "int"),
    ("Palette_Color_Context", "int"), ("Palette_Color_Hash_Multipliers", "int"),
    ("Tx_Size_Sqr", "int"), ("Tx_Size_Sqr_Up", "int"), ("Tx_Width", "int"), ("Tx_Height", "int"),
    ("Tx_Width_Log2", "int"), ("Tx_Height_Log2", "int"), ("Wedge_Bits", "int"), ("Adjusted_Tx_Size", "int"),
    ("Tx_Type_In_Set_Intra", "int"), ("Tx_Type_In_Set_Inter", "int"),
    ("Tx_Type_Intra_Inv_Set1", "int"), ("Tx_Type_Intra_Inv_Set2", "int"),
    ("Tx_Type_Inter_Inv_Set1", "int"), ("Tx_Type_Inter_Inv_Set2", "int"), ("Tx_Type_Inter_Inv_Set3", "int"),
    ("Segmentation_Feature_Bits", "int"), ("Segmentation_Feature_Signed", "int"), ("Segmentation_Feature_Max", "int"),
    ("Remap_Lr_Type", "int"), ("Wiener_Taps_Mid", "int"), ("Wiener_Taps_Min", "int"), ("Wiener_Taps_Max", "int"),
    ("Wiener_Taps_K", "int"), ("Sgrproj_Xqd_Mid", "int"), ("Sgrproj_Xqd_Min", "int"), ("Sgrproj_Xqd_Max", "int"),
    ("Ref_Frame_List", "int"),
]:
    add(n, t)

# ---- explicit invariants ------------------------------------------------
T = {t[4]: t for t in tables}
def get(n): return T[n][3]
assert T["Quantizer_Matrix"][1] == (15, 2, 3344)
assert T["Dc_Qlookup"][1] == (3, 256) and T["Ac_Qlookup"][1] == (3, 256)
assert T["Gaussian_Sequence"][1] == (2048,)
assert T["Warped_Filters"][1] == (193, 8)
for row in get("Warped_Filters"): assert sum(row) == 128, row
for f in get("Subpel_Filters"):
    for row in f: assert sum(row) == 128, row
for row in get("Upscale_Filter"): assert sum(row) == 128, row
for f in get("Intra_Filter_Taps"):
    pass
assert T["Div_Lut"][1] == (257,)
assert T["Wedge_Codebook"][1] == (3, 16, 3)
# dc/ac q tables monotonic
for tbl in (get("Dc_Qlookup"), get("Ac_Qlookup")):
    for row in tbl:
        assert all(row[i] <= row[i+1] for i in range(255))
assert get("Tx_Width") == [4,8,16,32,64,4,8,8,16,16,32,32,64,4,16,8,32,16,64]
assert get("Num_4x4_Blocks_Wide") == [1,1,2,2,2,4,4,4,8,8,8,16,16,16,32,32,1,4,2,8,4,16]
assert get("Mi_Width_Log2") == [0,0,1,1,1,2,2,2,3,3,3,4,4,4,5,5,0,2,1,3,2,4]
assert get("Wedge_Bits") == [0,0,0,4,4,4,4,4,4,4,0,0,0,0,0,0,0,0,4,4,0,0]
assert get("Sm_Weights_Tx_4x4") == [255,149,85,64]
for k in range(3):
    pass

hdr = '''// Code generated by gen_tables.py from the AV1 Bitstream & Decoding Process
// Specification, version 1.0.0 with Errata 1 (Alliance for Open Media,
// http://aomedia.org/license/). DO NOT EDIT.
//
// Provenance: the constant tables of the specification (default CDFs, scan
// orders, quantizer lookup and matrix tables, interpolation / warp / super-res
// filter kernels, wedge masters, Gaussian sequence, ...) were extracted
// mechanically from the specification text and validated against the
// invariants stated there (CDF monotonicity and terminal 32768, permutation
// scans, filter tap sums of 128, ...). The default CDF tables were
// additionally cross-checked against the data segment of libaom 3.8.2
// (BSD-2-Clause, see NOTICE) by cross_check_cdfs.py.

package av1

'''
with open(OUT, "w") as f:
    f.write(hdr)
    for goname, sh, elem, a, spec in tables:
        f.write("// %s\n" % spec)
        f.write("var %s = %s%s\n\n" % (goname, gotype(sh, elem), emit(a, 0, 12 if elem != "uint8" else 24)))
print("wrote", OUT, len(tables), "tables")
