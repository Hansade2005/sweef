package av1

// Hand-written tables from the specification text whose structure is not
// recoverable from the pdftotext rendering.

// subsampledSize is Subsampled_Size (spec 5.11.38): [block size][subX][subY].
var subsampledSize = [blockSizes][2][2]int{
	{{block4x4, block4x4}, {block4x4, block4x4}},
	{{block4x8, block4x4}, {blockInvalid, block4x4}},
	{{block8x4, blockInvalid}, {block4x4, block4x4}},
	{{block8x8, block8x4}, {block4x8, block4x4}},
	{{block8x16, block8x8}, {blockInvalid, block4x8}},
	{{block16x8, blockInvalid}, {block8x8, block8x4}},
	{{block16x16, block16x8}, {block8x16, block8x8}},
	{{block16x32, block16x16}, {blockInvalid, block8x16}},
	{{block32x16, blockInvalid}, {block16x16, block16x8}},
	{{block32x32, block32x16}, {block16x32, block16x16}},
	{{block32x64, block32x32}, {blockInvalid, block16x32}},
	{{block64x32, blockInvalid}, {block32x32, block32x16}},
	{{block64x64, block64x32}, {block32x64, block32x32}},
	{{block64x128, block64x64}, {blockInvalid, block32x64}},
	{{block128x64, blockInvalid}, {block64x64, block64x32}},
	{{block128x128, block128x64}, {block64x128, block64x64}},
	{{block4x16, block4x8}, {blockInvalid, block4x8}},
	{{block16x4, blockInvalid}, {block8x4, block8x4}},
	{{block8x32, block8x16}, {blockInvalid, block4x16}},
	{{block32x8, blockInvalid}, {block16x8, block16x4}},
	{{block16x64, block16x32}, {blockInvalid, block8x32}},
	{{block64x16, blockInvalid}, {block32x16, block32x8}},
}
