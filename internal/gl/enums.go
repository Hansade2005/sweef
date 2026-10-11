// Code generated from the GL constant table; DO NOT EDIT.
package gl

const (
	GL_DEPTH_BUFFER_BIT                              = 0x100
	GL_STENCIL_BUFFER_BIT                            = 0x400
	GL_COLOR_BUFFER_BIT                              = 0x4000
	GL_POINTS                                        = 0
	GL_LINES                                         = 1
	GL_LINE_LOOP                                     = 2
	GL_LINE_STRIP                                    = 3
	GL_TRIANGLES                                     = 4
	GL_TRIANGLE_STRIP                                = 5
	GL_TRIANGLE_FAN                                  = 6
	GL_ZERO                                          = 0
	GL_ONE                                           = 1
	GL_SRC_COLOR                                     = 0x300
	GL_ONE_MINUS_SRC_COLOR                           = 0x301
	GL_SRC_ALPHA                                     = 0x302
	GL_ONE_MINUS_SRC_ALPHA                           = 0x303
	GL_DST_ALPHA                                     = 0x304
	GL_ONE_MINUS_DST_ALPHA                           = 0x305
	GL_DST_COLOR                                     = 0x306
	GL_ONE_MINUS_DST_COLOR                           = 0x307
	GL_SRC_ALPHA_SATURATE                            = 0x308
	GL_FUNC_ADD                                      = 0x8006
	GL_BLEND_EQUATION                                = 0x8009
	GL_BLEND_EQUATION_RGB                            = 0x8009
	GL_BLEND_EQUATION_ALPHA                          = 0x883D
	GL_FUNC_SUBTRACT                                 = 0x800A
	GL_FUNC_REVERSE_SUBTRACT                         = 0x800B
	GL_BLEND_DST_RGB                                 = 0x80C8
	GL_BLEND_SRC_RGB                                 = 0x80C9
	GL_BLEND_DST_ALPHA                               = 0x80CA
	GL_BLEND_SRC_ALPHA                               = 0x80CB
	GL_CONSTANT_COLOR                                = 0x8001
	GL_ONE_MINUS_CONSTANT_COLOR                      = 0x8002
	GL_CONSTANT_ALPHA                                = 0x8003
	GL_ONE_MINUS_CONSTANT_ALPHA                      = 0x8004
	GL_BLEND_COLOR                                   = 0x8005
	GL_ARRAY_BUFFER                                  = 0x8892
	GL_ELEMENT_ARRAY_BUFFER                          = 0x8893
	GL_ARRAY_BUFFER_BINDING                          = 0x8894
	GL_ELEMENT_ARRAY_BUFFER_BINDING                  = 0x8895
	GL_STREAM_DRAW                                   = 0x88E0
	GL_STATIC_DRAW                                   = 0x88E4
	GL_DYNAMIC_DRAW                                  = 0x88E8
	GL_BUFFER_SIZE                                   = 0x8764
	GL_BUFFER_USAGE                                  = 0x8765
	GL_CURRENT_VERTEX_ATTRIB                         = 0x8626
	GL_FRONT                                         = 0x404
	GL_BACK                                          = 0x405
	GL_FRONT_AND_BACK                                = 0x408
	GL_TEXTURE_2D                                    = 0xDE1
	GL_CULL_FACE                                     = 0xB44
	GL_BLEND                                         = 0xBE2
	GL_DITHER                                        = 0xBD0
	GL_STENCIL_TEST                                  = 0xB90
	GL_DEPTH_TEST                                    = 0xB71
	GL_SCISSOR_TEST                                  = 0xC11
	GL_POLYGON_OFFSET_FILL                           = 0x8037
	GL_SAMPLE_ALPHA_TO_COVERAGE                      = 0x809E
	GL_SAMPLE_COVERAGE                               = 0x80A0
	GL_NO_ERROR                                      = 0
	GL_INVALID_ENUM                                  = 0x500
	GL_INVALID_VALUE                                 = 0x501
	GL_INVALID_OPERATION                             = 0x502
	GL_OUT_OF_MEMORY                                 = 0x505
	GL_CW                                            = 0x900
	GL_CCW                                           = 0x901
	GL_LINE_WIDTH                                    = 0xB21
	GL_ALIASED_POINT_SIZE_RANGE                      = 0x846D
	GL_ALIASED_LINE_WIDTH_RANGE                      = 0x846E
	GL_CULL_FACE_MODE                                = 0xB45
	GL_FRONT_FACE                                    = 0xB46
	GL_DEPTH_RANGE                                   = 0xB70
	GL_DEPTH_WRITEMASK                               = 0xB72
	GL_DEPTH_CLEAR_VALUE                             = 0xB73
	GL_DEPTH_FUNC                                    = 0xB74
	GL_STENCIL_CLEAR_VALUE                           = 0xB91
	GL_STENCIL_FUNC                                  = 0xB92
	GL_STENCIL_FAIL                                  = 0xB94
	GL_STENCIL_PASS_DEPTH_FAIL                       = 0xB95
	GL_STENCIL_PASS_DEPTH_PASS                       = 0xB96
	GL_STENCIL_REF                                   = 0xB97
	GL_STENCIL_VALUE_MASK                            = 0xB93
	GL_STENCIL_WRITEMASK                             = 0xB98
	GL_STENCIL_BACK_FUNC                             = 0x8800
	GL_STENCIL_BACK_FAIL                             = 0x8801
	GL_STENCIL_BACK_PASS_DEPTH_FAIL                  = 0x8802
	GL_STENCIL_BACK_PASS_DEPTH_PASS                  = 0x8803
	GL_STENCIL_BACK_REF                              = 0x8CA3
	GL_STENCIL_BACK_VALUE_MASK                       = 0x8CA4
	GL_STENCIL_BACK_WRITEMASK                        = 0x8CA5
	GL_VIEWPORT                                      = 0xBA2
	GL_SCISSOR_BOX                                   = 0xC10
	GL_COLOR_CLEAR_VALUE                             = 0xC22
	GL_COLOR_WRITEMASK                               = 0xC23
	GL_UNPACK_ALIGNMENT                              = 0xCF5
	GL_PACK_ALIGNMENT                                = 0xD05
	GL_MAX_TEXTURE_SIZE                              = 0xD33
	GL_MAX_VIEWPORT_DIMS                             = 0xD3A
	GL_SUBPIXEL_BITS                                 = 0xD50
	GL_RED_BITS                                      = 0xD52
	GL_GREEN_BITS                                    = 0xD53
	GL_BLUE_BITS                                     = 0xD54
	GL_ALPHA_BITS                                    = 0xD55
	GL_DEPTH_BITS                                    = 0xD56
	GL_STENCIL_BITS                                  = 0xD57
	GL_POLYGON_OFFSET_UNITS                          = 0x2A00
	GL_POLYGON_OFFSET_FACTOR                         = 0x8038
	GL_TEXTURE_BINDING_2D                            = 0x8069
	GL_SAMPLE_BUFFERS                                = 0x80A8
	GL_SAMPLES                                       = 0x80A9
	GL_SAMPLE_COVERAGE_VALUE                         = 0x80AA
	GL_SAMPLE_COVERAGE_INVERT                        = 0x80AB
	GL_COMPRESSED_TEXTURE_FORMATS                    = 0x86A3
	GL_DONT_CARE                                     = 0x1100
	GL_FASTEST                                       = 0x1101
	GL_NICEST                                        = 0x1102
	GL_GENERATE_MIPMAP_HINT                          = 0x8192
	GL_BYTE                                          = 0x1400
	GL_UNSIGNED_BYTE                                 = 0x1401
	GL_SHORT                                         = 0x1402
	GL_UNSIGNED_SHORT                                = 0x1403
	GL_INT                                           = 0x1404
	GL_UNSIGNED_INT                                  = 0x1405
	GL_FLOAT                                         = 0x1406
	GL_HALF_FLOAT                                    = 0x140B
	GL_FIXED                                         = 0x140C
	GL_DEPTH_COMPONENT                               = 0x1902
	GL_ALPHA                                         = 0x1906
	GL_RGB                                           = 0x1907
	GL_RGBA                                          = 0x1908
	GL_LUMINANCE                                     = 0x1909
	GL_LUMINANCE_ALPHA                               = 0x190A
	GL_UNSIGNED_SHORT_4_4_4_4                        = 0x8033
	GL_UNSIGNED_SHORT_5_5_5_1                        = 0x8034
	GL_UNSIGNED_SHORT_5_6_5                          = 0x8363
	GL_FRAGMENT_SHADER                               = 0x8B30
	GL_VERTEX_SHADER                                 = 0x8B31
	GL_MAX_VERTEX_ATTRIBS                            = 0x8869
	GL_MAX_VERTEX_UNIFORM_VECTORS                    = 0x8DFB
	GL_MAX_VARYING_VECTORS                           = 0x8DFC
	GL_MAX_COMBINED_TEXTURE_IMAGE_UNITS              = 0x8B4D
	GL_MAX_VERTEX_TEXTURE_IMAGE_UNITS                = 0x8B4C
	GL_MAX_TEXTURE_IMAGE_UNITS                       = 0x8872
	GL_MAX_FRAGMENT_UNIFORM_VECTORS                  = 0x8DFD
	GL_SHADER_TYPE                                   = 0x8B4F
	GL_DELETE_STATUS                                 = 0x8B80
	GL_LINK_STATUS                                   = 0x8B82
	GL_VALIDATE_STATUS                               = 0x8B83
	GL_ATTACHED_SHADERS                              = 0x8B85
	GL_ACTIVE_UNIFORMS                               = 0x8B86
	GL_ACTIVE_ATTRIBUTES                             = 0x8B89
	GL_SHADING_LANGUAGE_VERSION                      = 0x8B8C
	GL_CURRENT_PROGRAM                               = 0x8B8D
	GL_NEVER                                         = 0x200
	GL_LESS                                          = 0x201
	GL_EQUAL                                         = 0x202
	GL_LEQUAL                                        = 0x203
	GL_GREATER                                       = 0x204
	GL_NOTEQUAL                                      = 0x205
	GL_GEQUAL                                        = 0x206
	GL_ALWAYS                                        = 0x207
	GL_KEEP                                          = 0x1E00
	GL_REPLACE                                       = 0x1E01
	GL_INCR                                          = 0x1E02
	GL_DECR                                          = 0x1E03
	GL_INVERT                                        = 0x150A
	GL_INCR_WRAP                                     = 0x8507
	GL_DECR_WRAP                                     = 0x8508
	GL_VENDOR                                        = 0x1F00
	GL_RENDERER                                      = 0x1F01
	GL_VERSION                                       = 0x1F02
	GL_EXTENSIONS                                    = 0x1F03
	GL_NEAREST                                       = 0x2600
	GL_LINEAR                                        = 0x2601
	GL_NEAREST_MIPMAP_NEAREST                        = 0x2700
	GL_LINEAR_MIPMAP_NEAREST                         = 0x2701
	GL_NEAREST_MIPMAP_LINEAR                         = 0x2702
	GL_LINEAR_MIPMAP_LINEAR                          = 0x2703
	GL_TEXTURE_MAG_FILTER                            = 0x2800
	GL_TEXTURE_MIN_FILTER                            = 0x2801
	GL_TEXTURE_WRAP_S                                = 0x2802
	GL_TEXTURE_WRAP_T                                = 0x2803
	GL_TEXTURE                                       = 0x1702
	GL_TEXTURE_CUBE_MAP                              = 0x8513
	GL_TEXTURE_BINDING_CUBE_MAP                      = 0x8514
	GL_TEXTURE_CUBE_MAP_POSITIVE_X                   = 0x8515
	GL_TEXTURE_CUBE_MAP_NEGATIVE_X                   = 0x8516
	GL_TEXTURE_CUBE_MAP_POSITIVE_Y                   = 0x8517
	GL_TEXTURE_CUBE_MAP_NEGATIVE_Y                   = 0x8518
	GL_TEXTURE_CUBE_MAP_POSITIVE_Z                   = 0x8519
	GL_TEXTURE_CUBE_MAP_NEGATIVE_Z                   = 0x851A
	GL_MAX_CUBE_MAP_TEXTURE_SIZE                     = 0x851C
	GL_TEXTURE0                                      = 0x84C0
	GL_ACTIVE_TEXTURE                                = 0x84E0
	GL_REPEAT                                        = 0x2901
	GL_CLAMP_TO_EDGE                                 = 0x812F
	GL_MIRRORED_REPEAT                               = 0x8370
	GL_FLOAT_VEC2                                    = 0x8B50
	GL_FLOAT_VEC3                                    = 0x8B51
	GL_FLOAT_VEC4                                    = 0x8B52
	GL_INT_VEC2                                      = 0x8B53
	GL_INT_VEC3                                      = 0x8B54
	GL_INT_VEC4                                      = 0x8B55
	GL_BOOL                                          = 0x8B56
	GL_BOOL_VEC2                                     = 0x8B57
	GL_BOOL_VEC3                                     = 0x8B58
	GL_BOOL_VEC4                                     = 0x8B59
	GL_FLOAT_MAT2                                    = 0x8B5A
	GL_FLOAT_MAT3                                    = 0x8B5B
	GL_FLOAT_MAT4                                    = 0x8B5C
	GL_SAMPLER_2D                                    = 0x8B5E
	GL_SAMPLER_CUBE                                  = 0x8B60
	GL_VERTEX_ATTRIB_ARRAY_ENABLED                   = 0x8622
	GL_VERTEX_ATTRIB_ARRAY_SIZE                      = 0x8623
	GL_VERTEX_ATTRIB_ARRAY_STRIDE                    = 0x8624
	GL_VERTEX_ATTRIB_ARRAY_TYPE                      = 0x8625
	GL_VERTEX_ATTRIB_ARRAY_NORMALIZED                = 0x886A
	GL_VERTEX_ATTRIB_ARRAY_POINTER                   = 0x8645
	GL_VERTEX_ATTRIB_ARRAY_BUFFER_BINDING            = 0x889F
	GL_IMPLEMENTATION_COLOR_READ_TYPE                = 0x8B9A
	GL_IMPLEMENTATION_COLOR_READ_FORMAT              = 0x8B9B
	GL_COMPILE_STATUS                                = 0x8B81
	GL_LOW_FLOAT                                     = 0x8DF0
	GL_MEDIUM_FLOAT                                  = 0x8DF1
	GL_HIGH_FLOAT                                    = 0x8DF2
	GL_LOW_INT                                       = 0x8DF3
	GL_MEDIUM_INT                                    = 0x8DF4
	GL_HIGH_INT                                      = 0x8DF5
	GL_FRAMEBUFFER                                   = 0x8D40
	GL_RENDERBUFFER                                  = 0x8D41
	GL_RGBA4                                         = 0x8056
	GL_RGB5_A1                                       = 0x8057
	GL_RGB565                                        = 0x8D62
	GL_DEPTH_COMPONENT16                             = 0x81A5
	GL_STENCIL_INDEX8                                = 0x8D48
	GL_DEPTH_STENCIL                                 = 0x84F9
	GL_RENDERBUFFER_WIDTH                            = 0x8D42
	GL_RENDERBUFFER_HEIGHT                           = 0x8D43
	GL_RENDERBUFFER_INTERNAL_FORMAT                  = 0x8D44
	GL_RENDERBUFFER_RED_SIZE                         = 0x8D50
	GL_RENDERBUFFER_GREEN_SIZE                       = 0x8D51
	GL_RENDERBUFFER_BLUE_SIZE                        = 0x8D52
	GL_RENDERBUFFER_ALPHA_SIZE                       = 0x8D53
	GL_RENDERBUFFER_DEPTH_SIZE                       = 0x8D54
	GL_RENDERBUFFER_STENCIL_SIZE                     = 0x8D55
	GL_FRAMEBUFFER_ATTACHMENT_OBJECT_TYPE            = 0x8CD0
	GL_FRAMEBUFFER_ATTACHMENT_OBJECT_NAME            = 0x8CD1
	GL_FRAMEBUFFER_ATTACHMENT_TEXTURE_LEVEL          = 0x8CD2
	GL_FRAMEBUFFER_ATTACHMENT_TEXTURE_CUBE_MAP_FACE  = 0x8CD3
	GL_COLOR_ATTACHMENT0                             = 0x8CE0
	GL_DEPTH_ATTACHMENT                              = 0x8D00
	GL_STENCIL_ATTACHMENT                            = 0x8D20
	GL_DEPTH_STENCIL_ATTACHMENT                      = 0x821A
	GL_NONE                                          = 0
	GL_FRAMEBUFFER_COMPLETE                          = 0x8CD5
	GL_FRAMEBUFFER_INCOMPLETE_ATTACHMENT             = 0x8CD6
	GL_FRAMEBUFFER_INCOMPLETE_MISSING_ATTACHMENT     = 0x8CD7
	GL_FRAMEBUFFER_INCOMPLETE_DIMENSIONS             = 0x8CD9
	GL_FRAMEBUFFER_UNSUPPORTED                       = 0x8CDD
	GL_FRAMEBUFFER_BINDING                           = 0x8CA6
	GL_RENDERBUFFER_BINDING                          = 0x8CA7
	GL_MAX_RENDERBUFFER_SIZE                         = 0x84E8
	GL_INVALID_FRAMEBUFFER_OPERATION                 = 0x506
	GL_UNPACK_FLIP_Y_WEBGL                           = 0x9240
	GL_UNPACK_PREMULTIPLY_ALPHA_WEBGL                = 0x9241
	GL_CONTEXT_LOST_WEBGL                            = 0x9242
	GL_UNPACK_COLORSPACE_CONVERSION_WEBGL            = 0x9243
	GL_BROWSER_DEFAULT_WEBGL                         = 0x9244
	GL_READ_BUFFER                                   = 0x0C02
	GL_UNPACK_ROW_LENGTH                             = 0x0CF2
	GL_UNPACK_SKIP_ROWS                              = 0x0CF3
	GL_UNPACK_SKIP_PIXELS                            = 0x0CF4
	GL_PACK_ROW_LENGTH                               = 0x0D02
	GL_PACK_SKIP_ROWS                                = 0x0D03
	GL_PACK_SKIP_PIXELS                              = 0x0D04
	GL_COLOR                                         = 0x1800
	GL_DEPTH                                         = 0x1801
	GL_STENCIL                                       = 0x1802
	GL_RED                                           = 0x1903
	GL_RGB8                                          = 0x8051
	GL_RGBA8                                         = 0x8058
	GL_RGB10_A2                                      = 0x8059
	GL_TEXTURE_BINDING_3D                            = 0x806A
	GL_UNPACK_SKIP_IMAGES                            = 0x806D
	GL_UNPACK_IMAGE_HEIGHT                           = 0x806E
	GL_TEXTURE_3D                                    = 0x806F
	GL_TEXTURE_WRAP_R                                = 0x8072
	GL_MAX_3D_TEXTURE_SIZE                           = 0x8073
	GL_UNSIGNED_INT_2_10_10_10_REV                   = 0x8368
	GL_MAX_ELEMENTS_VERTICES                         = 0x80E8
	GL_MAX_ELEMENTS_INDICES                          = 0x80E9
	GL_TEXTURE_MIN_LOD                               = 0x813A
	GL_TEXTURE_MAX_LOD                               = 0x813B
	GL_TEXTURE_BASE_LEVEL                            = 0x813C
	GL_TEXTURE_MAX_LEVEL                             = 0x813D
	GL_MIN                                           = 0x8007
	GL_MAX                                           = 0x8008
	GL_DEPTH_COMPONENT24                             = 0x81A6
	GL_MAX_TEXTURE_LOD_BIAS                          = 0x84FD
	GL_TEXTURE_COMPARE_MODE                          = 0x884C
	GL_TEXTURE_COMPARE_FUNC                          = 0x884D
	GL_CURRENT_QUERY                                 = 0x8865
	GL_QUERY_RESULT                                  = 0x8866
	GL_QUERY_RESULT_AVAILABLE                        = 0x8867
	GL_STREAM_READ                                   = 0x88E1
	GL_STREAM_COPY                                   = 0x88E2
	GL_STATIC_READ                                   = 0x88E5
	GL_STATIC_COPY                                   = 0x88E6
	GL_DYNAMIC_READ                                  = 0x88E9
	GL_DYNAMIC_COPY                                  = 0x88EA
	GL_MAX_DRAW_BUFFERS                              = 0x8824
	GL_DRAW_BUFFER0                                  = 0x8825
	GL_MAX_FRAGMENT_UNIFORM_COMPONENTS               = 0x8B49
	GL_MAX_VERTEX_UNIFORM_COMPONENTS                 = 0x8B4A
	GL_SAMPLER_3D                                    = 0x8B5F
	GL_SAMPLER_2D_SHADOW                             = 0x8B62
	GL_FRAGMENT_SHADER_DERIVATIVE_HINT               = 0x8B8B
	GL_PIXEL_PACK_BUFFER                             = 0x88EB
	GL_PIXEL_UNPACK_BUFFER                           = 0x88EC
	GL_PIXEL_PACK_BUFFER_BINDING                     = 0x88ED
	GL_PIXEL_UNPACK_BUFFER_BINDING                   = 0x88EF
	GL_FLOAT_MAT2x3                                  = 0x8B65
	GL_FLOAT_MAT2x4                                  = 0x8B66
	GL_FLOAT_MAT3x2                                  = 0x8B67
	GL_FLOAT_MAT3x4                                  = 0x8B68
	GL_FLOAT_MAT4x2                                  = 0x8B69
	GL_FLOAT_MAT4x3                                  = 0x8B6A
	GL_SRGB                                          = 0x8C40
	GL_SRGB8                                         = 0x8C41
	GL_SRGB8_ALPHA8                                  = 0x8C43
	GL_COMPARE_REF_TO_TEXTURE                        = 0x884E
	GL_RGBA32F                                       = 0x8814
	GL_RGB32F                                        = 0x8815
	GL_RGBA16F                                       = 0x881A
	GL_RGB16F                                        = 0x881B
	GL_VERTEX_ATTRIB_ARRAY_INTEGER                   = 0x88FD
	GL_MAX_ARRAY_TEXTURE_LAYERS                      = 0x88FF
	GL_MIN_PROGRAM_TEXEL_OFFSET                      = 0x8904
	GL_MAX_PROGRAM_TEXEL_OFFSET                      = 0x8905
	GL_MAX_VARYING_COMPONENTS                        = 0x8B4B
	GL_TEXTURE_2D_ARRAY                              = 0x8C1A
	GL_TEXTURE_BINDING_2D_ARRAY                      = 0x8C1D
	GL_R11F_G11F_B10F                                = 0x8C3A
	GL_UNSIGNED_INT_10F_11F_11F_REV                  = 0x8C3B
	GL_RGB9_E5                                       = 0x8C3D
	GL_UNSIGNED_INT_5_9_9_9_REV                      = 0x8C3E
	GL_TRANSFORM_FEEDBACK_BUFFER_MODE                = 0x8C7F
	GL_MAX_TRANSFORM_FEEDBACK_SEPARATE_COMPONENTS    = 0x8C80
	GL_TRANSFORM_FEEDBACK_VARYINGS                   = 0x8C83
	GL_TRANSFORM_FEEDBACK_BUFFER_START               = 0x8C84
	GL_TRANSFORM_FEEDBACK_BUFFER_SIZE                = 0x8C85
	GL_TRANSFORM_FEEDBACK_PRIMITIVES_WRITTEN         = 0x8C88
	GL_RASTERIZER_DISCARD                            = 0x8C89
	GL_MAX_TRANSFORM_FEEDBACK_INTERLEAVED_COMPONENTS = 0x8C8A
	GL_MAX_TRANSFORM_FEEDBACK_SEPARATE_ATTRIBS       = 0x8C8B
	GL_INTERLEAVED_ATTRIBS                           = 0x8C8C
	GL_SEPARATE_ATTRIBS                              = 0x8C8D
	GL_TRANSFORM_FEEDBACK_BUFFER                     = 0x8C8E
	GL_TRANSFORM_FEEDBACK_BUFFER_BINDING             = 0x8C8F
	GL_TRANSFORM_FEEDBACK                            = 0x8E22
	GL_TRANSFORM_FEEDBACK_PAUSED                     = 0x8E23
	GL_TRANSFORM_FEEDBACK_ACTIVE                     = 0x8E24
	GL_TRANSFORM_FEEDBACK_BINDING                    = 0x8E25
	GL_FRAMEBUFFER_ATTACHMENT_COLOR_ENCODING         = 0x8210
	GL_FRAMEBUFFER_ATTACHMENT_COMPONENT_TYPE         = 0x8211
	GL_FRAMEBUFFER_ATTACHMENT_RED_SIZE               = 0x8212
	GL_FRAMEBUFFER_ATTACHMENT_GREEN_SIZE             = 0x8213
	GL_FRAMEBUFFER_ATTACHMENT_BLUE_SIZE              = 0x8214
	GL_FRAMEBUFFER_ATTACHMENT_ALPHA_SIZE             = 0x8215
	GL_FRAMEBUFFER_ATTACHMENT_DEPTH_SIZE             = 0x8216
	GL_FRAMEBUFFER_ATTACHMENT_STENCIL_SIZE           = 0x8217
	GL_FRAMEBUFFER_DEFAULT                           = 0x8218
	GL_DEPTH24_STENCIL8                              = 0x88F0
	GL_UNSIGNED_NORMALIZED                           = 0x8C17
	GL_DRAW_FRAMEBUFFER_BINDING                      = 0x8CA6
	GL_READ_FRAMEBUFFER                              = 0x8CA8
	GL_DRAW_FRAMEBUFFER                              = 0x8CA9
	GL_READ_FRAMEBUFFER_BINDING                      = 0x8CAA
	GL_RENDERBUFFER_SAMPLES                          = 0x8CAB
	GL_FRAMEBUFFER_ATTACHMENT_TEXTURE_LAYER          = 0x8CD4
	GL_MAX_COLOR_ATTACHMENTS                         = 0x8CDF
	GL_COLOR_ATTACHMENT1                             = 0x8CE1
	GL_COLOR_ATTACHMENT2                             = 0x8CE2
	GL_COLOR_ATTACHMENT3                             = 0x8CE3
	GL_COLOR_ATTACHMENT4                             = 0x8CE4
	GL_COLOR_ATTACHMENT5                             = 0x8CE5
	GL_COLOR_ATTACHMENT6                             = 0x8CE6
	GL_COLOR_ATTACHMENT7                             = 0x8CE7
	GL_COLOR_ATTACHMENT8                             = 0x8CE8
	GL_COLOR_ATTACHMENT9                             = 0x8CE9
	GL_COLOR_ATTACHMENT10                            = 0x8CEA
	GL_COLOR_ATTACHMENT11                            = 0x8CEB
	GL_COLOR_ATTACHMENT12                            = 0x8CEC
	GL_COLOR_ATTACHMENT13                            = 0x8CED
	GL_COLOR_ATTACHMENT14                            = 0x8CEE
	GL_COLOR_ATTACHMENT15                            = 0x8CEF
	GL_FRAMEBUFFER_INCOMPLETE_MULTISAMPLE            = 0x8D56
	GL_MAX_SAMPLES                                   = 0x8D57
	GL_HALF_FLOAT_OES                                = 0x8D61
	GL_RG                                            = 0x8227
	GL_RG_INTEGER                                    = 0x8228
	GL_R8                                            = 0x8229
	GL_RG8                                           = 0x822B
	GL_R16F                                          = 0x822D
	GL_R32F                                          = 0x822E
	GL_RG16F                                         = 0x822F
	GL_RG32F                                         = 0x8230
	GL_R8I                                           = 0x8231
	GL_R8UI                                          = 0x8232
	GL_R16I                                          = 0x8233
	GL_R16UI                                         = 0x8234
	GL_R32I                                          = 0x8235
	GL_R32UI                                         = 0x8236
	GL_RG8I                                          = 0x8237
	GL_RG8UI                                         = 0x8238
	GL_RG16I                                         = 0x8239
	GL_RG16UI                                        = 0x823A
	GL_RG32I                                         = 0x823B
	GL_RG32UI                                        = 0x823C
	GL_VERTEX_ARRAY_BINDING                          = 0x85B5
	GL_R8_SNORM                                      = 0x8F94
	GL_RG8_SNORM                                     = 0x8F95
	GL_RGB8_SNORM                                    = 0x8F96
	GL_RGBA8_SNORM                                   = 0x8F97
	GL_SIGNED_NORMALIZED                             = 0x8F9C
	GL_COPY_READ_BUFFER                              = 0x8F36
	GL_COPY_WRITE_BUFFER                             = 0x8F37
	GL_COPY_READ_BUFFER_BINDING                      = 0x8F36
	GL_COPY_WRITE_BUFFER_BINDING                     = 0x8F37
	GL_UNIFORM_BUFFER                                = 0x8A11
	GL_UNIFORM_BUFFER_BINDING                        = 0x8A28
	GL_UNIFORM_BUFFER_START                          = 0x8A29
	GL_UNIFORM_BUFFER_SIZE                           = 0x8A2A
	GL_MAX_VERTEX_UNIFORM_BLOCKS                     = 0x8A2B
	GL_MAX_FRAGMENT_UNIFORM_BLOCKS                   = 0x8A2D
	GL_MAX_COMBINED_UNIFORM_BLOCKS                   = 0x8A2E
	GL_MAX_UNIFORM_BUFFER_BINDINGS                   = 0x8A2F
	GL_MAX_UNIFORM_BLOCK_SIZE                        = 0x8A30
	GL_MAX_COMBINED_VERTEX_UNIFORM_COMPONENTS        = 0x8A31
	GL_MAX_COMBINED_FRAGMENT_UNIFORM_COMPONENTS      = 0x8A33
	GL_UNIFORM_BUFFER_OFFSET_ALIGNMENT               = 0x8A34
	GL_ACTIVE_UNIFORM_BLOCKS                         = 0x8A36
	GL_UNIFORM_TYPE                                  = 0x8A37
	GL_UNIFORM_SIZE                                  = 0x8A38
	GL_UNIFORM_BLOCK_INDEX                           = 0x8A3A
	GL_UNIFORM_OFFSET                                = 0x8A3B
	GL_UNIFORM_ARRAY_STRIDE                          = 0x8A3C
	GL_UNIFORM_MATRIX_STRIDE                         = 0x8A3D
	GL_UNIFORM_IS_ROW_MAJOR                          = 0x8A3E
	GL_UNIFORM_BLOCK_BINDING                         = 0x8A3F
	GL_UNIFORM_BLOCK_DATA_SIZE                       = 0x8A40
	GL_UNIFORM_BLOCK_ACTIVE_UNIFORMS                 = 0x8A42
	GL_UNIFORM_BLOCK_ACTIVE_UNIFORM_INDICES          = 0x8A43
	GL_UNIFORM_BLOCK_REFERENCED_BY_VERTEX_SHADER     = 0x8A44
	GL_UNIFORM_BLOCK_REFERENCED_BY_FRAGMENT_SHADER   = 0x8A46
	GL_INVALID_INDEX                                 = 0xFFFFFFFF
	GL_MAX_VERTEX_OUTPUT_COMPONENTS                  = 0x9122
	GL_MAX_FRAGMENT_INPUT_COMPONENTS                 = 0x9125
	GL_MAX_SERVER_WAIT_TIMEOUT                       = 0x9111
	GL_OBJECT_TYPE                                   = 0x9112
	GL_SYNC_CONDITION                                = 0x9113
	GL_SYNC_STATUS                                   = 0x9114
	GL_SYNC_FLAGS                                    = 0x9115
	GL_SYNC_FENCE                                    = 0x9116
	GL_SYNC_GPU_COMMANDS_COMPLETE                    = 0x9117
	GL_UNSIGNALED                                    = 0x9118
	GL_SIGNALED                                      = 0x9119
	GL_ALREADY_SIGNALED                              = 0x911A
	GL_TIMEOUT_EXPIRED                               = 0x911B
	GL_CONDITION_SATISFIED                           = 0x911C
	GL_WAIT_FAILED                                   = 0x911D
	GL_SYNC_FLUSH_COMMANDS_BIT                       = 0x1
	GL_VERTEX_ATTRIB_ARRAY_DIVISOR                   = 0x88FE
	GL_ANY_SAMPLES_PASSED                            = 0x8C2F
	GL_ANY_SAMPLES_PASSED_CONSERVATIVE               = 0x8D6A
	GL_SAMPLER_BINDING                               = 0x8919
	GL_RGB10_A2UI                                    = 0x906F
	GL_INT_2_10_10_10_REV                            = 0x8D9F
	GL_RGBA32UI                                      = 0x8D70
	GL_RGB32UI                                       = 0x8D71
	GL_RGBA16UI                                      = 0x8D76
	GL_RGB16UI                                       = 0x8D77
	GL_RGBA8UI                                       = 0x8D7C
	GL_RGB8UI                                        = 0x8D7D
	GL_RGBA32I                                       = 0x8D82
	GL_RGB32I                                        = 0x8D83
	GL_RGBA16I                                       = 0x8D88
	GL_RGB16I                                        = 0x8D89
	GL_RGBA8I                                        = 0x8D8E
	GL_RGB8I                                         = 0x8D8F
	GL_RED_INTEGER                                   = 0x8D94
	GL_RGB_INTEGER                                   = 0x8D98
	GL_RGBA_INTEGER                                  = 0x8D99
	GL_SAMPLER_2D_ARRAY                              = 0x8DC1
	GL_SAMPLER_2D_ARRAY_SHADOW                       = 0x8DC4
	GL_SAMPLER_CUBE_SHADOW                           = 0x8DC5
	GL_UNSIGNED_INT_VEC2                             = 0x8DC6
	GL_UNSIGNED_INT_VEC3                             = 0x8DC7
	GL_UNSIGNED_INT_VEC4                             = 0x8DC8
	GL_INT_SAMPLER_2D                                = 0x8DCA
	GL_INT_SAMPLER_3D                                = 0x8DCB
	GL_INT_SAMPLER_CUBE                              = 0x8DCC
	GL_INT_SAMPLER_2D_ARRAY                          = 0x8DCF
	GL_UNSIGNED_INT_SAMPLER_2D                       = 0x8DD2
	GL_UNSIGNED_INT_SAMPLER_3D                       = 0x8DD3
	GL_UNSIGNED_INT_SAMPLER_CUBE                     = 0x8DD4
	GL_UNSIGNED_INT_SAMPLER_2D_ARRAY                 = 0x8DD7
	GL_DEPTH_COMPONENT32F                            = 0x8CAC
	GL_DEPTH32F_STENCIL8                             = 0x8CAD
	GL_FLOAT_32_UNSIGNED_INT_24_8_REV                = 0x8DAD
	GL_UNSIGNED_INT_24_8                             = 0x84FA
	GL_DEPTH_STENCIL_UNSIGNED_INT_24_8_WEBGL         = 0x84FA
	GL_TEXTURE_IMMUTABLE_FORMAT                      = 0x912F
	GL_TEXTURE_IMMUTABLE_LEVELS                      = 0x82DF
	GL_MAX_CLIENT_WAIT_TIMEOUT_WEBGL                 = 0x9247
	GL_MAX_TEXTURE_MAX_ANISOTROPY_EXT                = 0x84FF
	GL_TEXTURE_MAX_ANISOTROPY_EXT                    = 0x84FE
	GL_UNMASKED_VENDOR_WEBGL                         = 0x9245
	GL_UNMASKED_RENDERER_WEBGL                       = 0x9246
	GL_MAX_DRAW_BUFFERS_WEBGL                        = 0x8824
	GL_VERTEX_ARRAY_BINDING_OES                      = 0x85B5
	GL_VERTEX_ATTRIB_ARRAY_DIVISOR_ANGLE             = 0x88FE
	GL_FRAGMENT_SHADER_DERIVATIVE_HINT_OES           = 0x8B8B
	GL_MIN_EXT                                       = 0x8007
	GL_MAX_EXT                                       = 0x8008
	GL_SRGB_EXT                                      = 0x8C40
	GL_SRGB_ALPHA_EXT                                = 0x8C42
	GL_SRGB8_ALPHA8_EXT                              = 0x8C43
	GL_RGBA32F_EXT                                   = 0x8814
	GL_RGB32F_EXT                                    = 0x8815
	GL_RGBA16F_EXT                                   = 0x881A
	GL_RGB16F_EXT                                    = 0x881B
	GL_UNSIGNED_NORMALIZED_EXT                       = 0x8C17
	GL_FRAMEBUFFER_ATTACHMENT_COMPONENT_TYPE_EXT     = 0x8211
	GL_FRAMEBUFFER_ATTACHMENT_COLOR_ENCODING_EXT     = 0x8210
	GL_UNSIGNED_INT_24_8_WEBGL                       = 0x84FA
	GL_QUERY_COUNTER_BITS_EXT                        = 0x8864
	GL_TIME_ELAPSED_EXT                              = 0x88BF
	GL_TIMESTAMP_EXT                                 = 0x8E28
	GL_GPU_DISJOINT_EXT                              = 0x8FBB
	GL_COMPLETION_STATUS_KHR                         = 0x91B1
)

// Constants1 lists constant names and values.
var Constants1 = map[string]uint32{
	"DEPTH_BUFFER_BIT":                     0x100,
	"STENCIL_BUFFER_BIT":                   0x400,
	"COLOR_BUFFER_BIT":                     0x4000,
	"POINTS":                               0,
	"LINES":                                1,
	"LINE_LOOP":                            2,
	"LINE_STRIP":                           3,
	"TRIANGLES":                            4,
	"TRIANGLE_STRIP":                       5,
	"TRIANGLE_FAN":                         6,
	"ZERO":                                 0,
	"ONE":                                  1,
	"SRC_COLOR":                            0x300,
	"ONE_MINUS_SRC_COLOR":                  0x301,
	"SRC_ALPHA":                            0x302,
	"ONE_MINUS_SRC_ALPHA":                  0x303,
	"DST_ALPHA":                            0x304,
	"ONE_MINUS_DST_ALPHA":                  0x305,
	"DST_COLOR":                            0x306,
	"ONE_MINUS_DST_COLOR":                  0x307,
	"SRC_ALPHA_SATURATE":                   0x308,
	"FUNC_ADD":                             0x8006,
	"BLEND_EQUATION":                       0x8009,
	"BLEND_EQUATION_RGB":                   0x8009,
	"BLEND_EQUATION_ALPHA":                 0x883D,
	"FUNC_SUBTRACT":                        0x800A,
	"FUNC_REVERSE_SUBTRACT":                0x800B,
	"BLEND_DST_RGB":                        0x80C8,
	"BLEND_SRC_RGB":                        0x80C9,
	"BLEND_DST_ALPHA":                      0x80CA,
	"BLEND_SRC_ALPHA":                      0x80CB,
	"CONSTANT_COLOR":                       0x8001,
	"ONE_MINUS_CONSTANT_COLOR":             0x8002,
	"CONSTANT_ALPHA":                       0x8003,
	"ONE_MINUS_CONSTANT_ALPHA":             0x8004,
	"BLEND_COLOR":                          0x8005,
	"ARRAY_BUFFER":                         0x8892,
	"ELEMENT_ARRAY_BUFFER":                 0x8893,
	"ARRAY_BUFFER_BINDING":                 0x8894,
	"ELEMENT_ARRAY_BUFFER_BINDING":         0x8895,
	"STREAM_DRAW":                          0x88E0,
	"STATIC_DRAW":                          0x88E4,
	"DYNAMIC_DRAW":                         0x88E8,
	"BUFFER_SIZE":                          0x8764,
	"BUFFER_USAGE":                         0x8765,
	"CURRENT_VERTEX_ATTRIB":                0x8626,
	"FRONT":                                0x404,
	"BACK":                                 0x405,
	"FRONT_AND_BACK":                       0x408,
	"TEXTURE_2D":                           0xDE1,
	"CULL_FACE":                            0xB44,
	"BLEND":                                0xBE2,
	"DITHER":                               0xBD0,
	"STENCIL_TEST":                         0xB90,
	"DEPTH_TEST":                           0xB71,
	"SCISSOR_TEST":                         0xC11,
	"POLYGON_OFFSET_FILL":                  0x8037,
	"SAMPLE_ALPHA_TO_COVERAGE":             0x809E,
	"SAMPLE_COVERAGE":                      0x80A0,
	"NO_ERROR":                             0,
	"INVALID_ENUM":                         0x500,
	"INVALID_VALUE":                        0x501,
	"INVALID_OPERATION":                    0x502,
	"OUT_OF_MEMORY":                        0x505,
	"CW":                                   0x900,
	"CCW":                                  0x901,
	"LINE_WIDTH":                           0xB21,
	"ALIASED_POINT_SIZE_RANGE":             0x846D,
	"ALIASED_LINE_WIDTH_RANGE":             0x846E,
	"CULL_FACE_MODE":                       0xB45,
	"FRONT_FACE":                           0xB46,
	"DEPTH_RANGE":                          0xB70,
	"DEPTH_WRITEMASK":                      0xB72,
	"DEPTH_CLEAR_VALUE":                    0xB73,
	"DEPTH_FUNC":                           0xB74,
	"STENCIL_CLEAR_VALUE":                  0xB91,
	"STENCIL_FUNC":                         0xB92,
	"STENCIL_FAIL":                         0xB94,
	"STENCIL_PASS_DEPTH_FAIL":              0xB95,
	"STENCIL_PASS_DEPTH_PASS":              0xB96,
	"STENCIL_REF":                          0xB97,
	"STENCIL_VALUE_MASK":                   0xB93,
	"STENCIL_WRITEMASK":                    0xB98,
	"STENCIL_BACK_FUNC":                    0x8800,
	"STENCIL_BACK_FAIL":                    0x8801,
	"STENCIL_BACK_PASS_DEPTH_FAIL":         0x8802,
	"STENCIL_BACK_PASS_DEPTH_PASS":         0x8803,
	"STENCIL_BACK_REF":                     0x8CA3,
	"STENCIL_BACK_VALUE_MASK":              0x8CA4,
	"STENCIL_BACK_WRITEMASK":               0x8CA5,
	"VIEWPORT":                             0xBA2,
	"SCISSOR_BOX":                          0xC10,
	"COLOR_CLEAR_VALUE":                    0xC22,
	"COLOR_WRITEMASK":                      0xC23,
	"UNPACK_ALIGNMENT":                     0xCF5,
	"PACK_ALIGNMENT":                       0xD05,
	"MAX_TEXTURE_SIZE":                     0xD33,
	"MAX_VIEWPORT_DIMS":                    0xD3A,
	"SUBPIXEL_BITS":                        0xD50,
	"RED_BITS":                             0xD52,
	"GREEN_BITS":                           0xD53,
	"BLUE_BITS":                            0xD54,
	"ALPHA_BITS":                           0xD55,
	"DEPTH_BITS":                           0xD56,
	"STENCIL_BITS":                         0xD57,
	"POLYGON_OFFSET_UNITS":                 0x2A00,
	"POLYGON_OFFSET_FACTOR":                0x8038,
	"TEXTURE_BINDING_2D":                   0x8069,
	"SAMPLE_BUFFERS":                       0x80A8,
	"SAMPLES":                              0x80A9,
	"SAMPLE_COVERAGE_VALUE":                0x80AA,
	"SAMPLE_COVERAGE_INVERT":               0x80AB,
	"COMPRESSED_TEXTURE_FORMATS":           0x86A3,
	"DONT_CARE":                            0x1100,
	"FASTEST":                              0x1101,
	"NICEST":                               0x1102,
	"GENERATE_MIPMAP_HINT":                 0x8192,
	"BYTE":                                 0x1400,
	"UNSIGNED_BYTE":                        0x1401,
	"SHORT":                                0x1402,
	"UNSIGNED_SHORT":                       0x1403,
	"INT":                                  0x1404,
	"UNSIGNED_INT":                         0x1405,
	"FLOAT":                                0x1406,
	"HALF_FLOAT":                           0x140B,
	"FIXED":                                0x140C,
	"DEPTH_COMPONENT":                      0x1902,
	"ALPHA":                                0x1906,
	"RGB":                                  0x1907,
	"RGBA":                                 0x1908,
	"LUMINANCE":                            0x1909,
	"LUMINANCE_ALPHA":                      0x190A,
	"UNSIGNED_SHORT_4_4_4_4":               0x8033,
	"UNSIGNED_SHORT_5_5_5_1":               0x8034,
	"UNSIGNED_SHORT_5_6_5":                 0x8363,
	"FRAGMENT_SHADER":                      0x8B30,
	"VERTEX_SHADER":                        0x8B31,
	"MAX_VERTEX_ATTRIBS":                   0x8869,
	"MAX_VERTEX_UNIFORM_VECTORS":           0x8DFB,
	"MAX_VARYING_VECTORS":                  0x8DFC,
	"MAX_COMBINED_TEXTURE_IMAGE_UNITS":     0x8B4D,
	"MAX_VERTEX_TEXTURE_IMAGE_UNITS":       0x8B4C,
	"MAX_TEXTURE_IMAGE_UNITS":              0x8872,
	"MAX_FRAGMENT_UNIFORM_VECTORS":         0x8DFD,
	"SHADER_TYPE":                          0x8B4F,
	"DELETE_STATUS":                        0x8B80,
	"LINK_STATUS":                          0x8B82,
	"VALIDATE_STATUS":                      0x8B83,
	"ATTACHED_SHADERS":                     0x8B85,
	"ACTIVE_UNIFORMS":                      0x8B86,
	"ACTIVE_ATTRIBUTES":                    0x8B89,
	"SHADING_LANGUAGE_VERSION":             0x8B8C,
	"CURRENT_PROGRAM":                      0x8B8D,
	"NEVER":                                0x200,
	"LESS":                                 0x201,
	"EQUAL":                                0x202,
	"LEQUAL":                               0x203,
	"GREATER":                              0x204,
	"NOTEQUAL":                             0x205,
	"GEQUAL":                               0x206,
	"ALWAYS":                               0x207,
	"KEEP":                                 0x1E00,
	"REPLACE":                              0x1E01,
	"INCR":                                 0x1E02,
	"DECR":                                 0x1E03,
	"INVERT":                               0x150A,
	"INCR_WRAP":                            0x8507,
	"DECR_WRAP":                            0x8508,
	"VENDOR":                               0x1F00,
	"RENDERER":                             0x1F01,
	"VERSION":                              0x1F02,
	"EXTENSIONS":                           0x1F03,
	"NEAREST":                              0x2600,
	"LINEAR":                               0x2601,
	"NEAREST_MIPMAP_NEAREST":               0x2700,
	"LINEAR_MIPMAP_NEAREST":                0x2701,
	"NEAREST_MIPMAP_LINEAR":                0x2702,
	"LINEAR_MIPMAP_LINEAR":                 0x2703,
	"TEXTURE_MAG_FILTER":                   0x2800,
	"TEXTURE_MIN_FILTER":                   0x2801,
	"TEXTURE_WRAP_S":                       0x2802,
	"TEXTURE_WRAP_T":                       0x2803,
	"TEXTURE":                              0x1702,
	"TEXTURE_CUBE_MAP":                     0x8513,
	"TEXTURE_BINDING_CUBE_MAP":             0x8514,
	"TEXTURE_CUBE_MAP_POSITIVE_X":          0x8515,
	"TEXTURE_CUBE_MAP_NEGATIVE_X":          0x8516,
	"TEXTURE_CUBE_MAP_POSITIVE_Y":          0x8517,
	"TEXTURE_CUBE_MAP_NEGATIVE_Y":          0x8518,
	"TEXTURE_CUBE_MAP_POSITIVE_Z":          0x8519,
	"TEXTURE_CUBE_MAP_NEGATIVE_Z":          0x851A,
	"MAX_CUBE_MAP_TEXTURE_SIZE":            0x851C,
	"TEXTURE0":                             0x84C0,
	"ACTIVE_TEXTURE":                       0x84E0,
	"REPEAT":                               0x2901,
	"CLAMP_TO_EDGE":                        0x812F,
	"MIRRORED_REPEAT":                      0x8370,
	"FLOAT_VEC2":                           0x8B50,
	"FLOAT_VEC3":                           0x8B51,
	"FLOAT_VEC4":                           0x8B52,
	"INT_VEC2":                             0x8B53,
	"INT_VEC3":                             0x8B54,
	"INT_VEC4":                             0x8B55,
	"BOOL":                                 0x8B56,
	"BOOL_VEC2":                            0x8B57,
	"BOOL_VEC3":                            0x8B58,
	"BOOL_VEC4":                            0x8B59,
	"FLOAT_MAT2":                           0x8B5A,
	"FLOAT_MAT3":                           0x8B5B,
	"FLOAT_MAT4":                           0x8B5C,
	"SAMPLER_2D":                           0x8B5E,
	"SAMPLER_CUBE":                         0x8B60,
	"VERTEX_ATTRIB_ARRAY_ENABLED":          0x8622,
	"VERTEX_ATTRIB_ARRAY_SIZE":             0x8623,
	"VERTEX_ATTRIB_ARRAY_STRIDE":           0x8624,
	"VERTEX_ATTRIB_ARRAY_TYPE":             0x8625,
	"VERTEX_ATTRIB_ARRAY_NORMALIZED":       0x886A,
	"VERTEX_ATTRIB_ARRAY_POINTER":          0x8645,
	"VERTEX_ATTRIB_ARRAY_BUFFER_BINDING":   0x889F,
	"IMPLEMENTATION_COLOR_READ_TYPE":       0x8B9A,
	"IMPLEMENTATION_COLOR_READ_FORMAT":     0x8B9B,
	"COMPILE_STATUS":                       0x8B81,
	"LOW_FLOAT":                            0x8DF0,
	"MEDIUM_FLOAT":                         0x8DF1,
	"HIGH_FLOAT":                           0x8DF2,
	"LOW_INT":                              0x8DF3,
	"MEDIUM_INT":                           0x8DF4,
	"HIGH_INT":                             0x8DF5,
	"FRAMEBUFFER":                          0x8D40,
	"RENDERBUFFER":                         0x8D41,
	"RGBA4":                                0x8056,
	"RGB5_A1":                              0x8057,
	"RGB565":                               0x8D62,
	"DEPTH_COMPONENT16":                    0x81A5,
	"STENCIL_INDEX8":                       0x8D48,
	"DEPTH_STENCIL":                        0x84F9,
	"RENDERBUFFER_WIDTH":                   0x8D42,
	"RENDERBUFFER_HEIGHT":                  0x8D43,
	"RENDERBUFFER_INTERNAL_FORMAT":         0x8D44,
	"RENDERBUFFER_RED_SIZE":                0x8D50,
	"RENDERBUFFER_GREEN_SIZE":              0x8D51,
	"RENDERBUFFER_BLUE_SIZE":               0x8D52,
	"RENDERBUFFER_ALPHA_SIZE":              0x8D53,
	"RENDERBUFFER_DEPTH_SIZE":              0x8D54,
	"RENDERBUFFER_STENCIL_SIZE":            0x8D55,
	"FRAMEBUFFER_ATTACHMENT_OBJECT_TYPE":   0x8CD0,
	"FRAMEBUFFER_ATTACHMENT_OBJECT_NAME":   0x8CD1,
	"FRAMEBUFFER_ATTACHMENT_TEXTURE_LEVEL": 0x8CD2,
	"FRAMEBUFFER_ATTACHMENT_TEXTURE_CUBE_MAP_FACE": 0x8CD3,
	"COLOR_ATTACHMENT0":                            0x8CE0,
	"DEPTH_ATTACHMENT":                             0x8D00,
	"STENCIL_ATTACHMENT":                           0x8D20,
	"DEPTH_STENCIL_ATTACHMENT":                     0x821A,
	"NONE":                                         0,
	"FRAMEBUFFER_COMPLETE":                         0x8CD5,
	"FRAMEBUFFER_INCOMPLETE_ATTACHMENT":            0x8CD6,
	"FRAMEBUFFER_INCOMPLETE_MISSING_ATTACHMENT":    0x8CD7,
	"FRAMEBUFFER_INCOMPLETE_DIMENSIONS":            0x8CD9,
	"FRAMEBUFFER_UNSUPPORTED":                      0x8CDD,
	"FRAMEBUFFER_BINDING":                          0x8CA6,
	"RENDERBUFFER_BINDING":                         0x8CA7,
	"MAX_RENDERBUFFER_SIZE":                        0x84E8,
	"INVALID_FRAMEBUFFER_OPERATION":                0x506,
	"UNPACK_FLIP_Y_WEBGL":                          0x9240,
	"UNPACK_PREMULTIPLY_ALPHA_WEBGL":               0x9241,
	"CONTEXT_LOST_WEBGL":                           0x9242,
	"UNPACK_COLORSPACE_CONVERSION_WEBGL":           0x9243,
	"BROWSER_DEFAULT_WEBGL":                        0x9244,
}

// Constants2 lists constant names and values.
var Constants2 = map[string]uint32{
	"READ_BUFFER":                     0x0C02,
	"UNPACK_ROW_LENGTH":               0x0CF2,
	"UNPACK_SKIP_ROWS":                0x0CF3,
	"UNPACK_SKIP_PIXELS":              0x0CF4,
	"PACK_ROW_LENGTH":                 0x0D02,
	"PACK_SKIP_ROWS":                  0x0D03,
	"PACK_SKIP_PIXELS":                0x0D04,
	"COLOR":                           0x1800,
	"DEPTH":                           0x1801,
	"STENCIL":                         0x1802,
	"RED":                             0x1903,
	"RGB8":                            0x8051,
	"RGBA8":                           0x8058,
	"RGB10_A2":                        0x8059,
	"TEXTURE_BINDING_3D":              0x806A,
	"UNPACK_SKIP_IMAGES":              0x806D,
	"UNPACK_IMAGE_HEIGHT":             0x806E,
	"TEXTURE_3D":                      0x806F,
	"TEXTURE_WRAP_R":                  0x8072,
	"MAX_3D_TEXTURE_SIZE":             0x8073,
	"UNSIGNED_INT_2_10_10_10_REV":     0x8368,
	"MAX_ELEMENTS_VERTICES":           0x80E8,
	"MAX_ELEMENTS_INDICES":            0x80E9,
	"TEXTURE_MIN_LOD":                 0x813A,
	"TEXTURE_MAX_LOD":                 0x813B,
	"TEXTURE_BASE_LEVEL":              0x813C,
	"TEXTURE_MAX_LEVEL":               0x813D,
	"MIN":                             0x8007,
	"MAX":                             0x8008,
	"DEPTH_COMPONENT24":               0x81A6,
	"MAX_TEXTURE_LOD_BIAS":            0x84FD,
	"TEXTURE_COMPARE_MODE":            0x884C,
	"TEXTURE_COMPARE_FUNC":            0x884D,
	"CURRENT_QUERY":                   0x8865,
	"QUERY_RESULT":                    0x8866,
	"QUERY_RESULT_AVAILABLE":          0x8867,
	"STREAM_READ":                     0x88E1,
	"STREAM_COPY":                     0x88E2,
	"STATIC_READ":                     0x88E5,
	"STATIC_COPY":                     0x88E6,
	"DYNAMIC_READ":                    0x88E9,
	"DYNAMIC_COPY":                    0x88EA,
	"MAX_DRAW_BUFFERS":                0x8824,
	"DRAW_BUFFER0":                    0x8825,
	"MAX_FRAGMENT_UNIFORM_COMPONENTS": 0x8B49,
	"MAX_VERTEX_UNIFORM_COMPONENTS":   0x8B4A,
	"SAMPLER_3D":                      0x8B5F,
	"SAMPLER_2D_SHADOW":               0x8B62,
	"FRAGMENT_SHADER_DERIVATIVE_HINT": 0x8B8B,
	"PIXEL_PACK_BUFFER":               0x88EB,
	"PIXEL_UNPACK_BUFFER":             0x88EC,
	"PIXEL_PACK_BUFFER_BINDING":       0x88ED,
	"PIXEL_UNPACK_BUFFER_BINDING":     0x88EF,
	"FLOAT_MAT2x3":                    0x8B65,
	"FLOAT_MAT2x4":                    0x8B66,
	"FLOAT_MAT3x2":                    0x8B67,
	"FLOAT_MAT3x4":                    0x8B68,
	"FLOAT_MAT4x2":                    0x8B69,
	"FLOAT_MAT4x3":                    0x8B6A,
	"SRGB":                            0x8C40,
	"SRGB8":                           0x8C41,
	"SRGB8_ALPHA8":                    0x8C43,
	"COMPARE_REF_TO_TEXTURE":          0x884E,
	"RGBA32F":                         0x8814,
	"RGB32F":                          0x8815,
	"RGBA16F":                         0x881A,
	"RGB16F":                          0x881B,
	"VERTEX_ATTRIB_ARRAY_INTEGER":     0x88FD,
	"MAX_ARRAY_TEXTURE_LAYERS":        0x88FF,
	"MIN_PROGRAM_TEXEL_OFFSET":        0x8904,
	"MAX_PROGRAM_TEXEL_OFFSET":        0x8905,
	"MAX_VARYING_COMPONENTS":          0x8B4B,
	"TEXTURE_2D_ARRAY":                0x8C1A,
	"TEXTURE_BINDING_2D_ARRAY":        0x8C1D,
	"R11F_G11F_B10F":                  0x8C3A,
	"UNSIGNED_INT_10F_11F_11F_REV":    0x8C3B,
	"RGB9_E5":                         0x8C3D,
	"UNSIGNED_INT_5_9_9_9_REV":        0x8C3E,
	"TRANSFORM_FEEDBACK_BUFFER_MODE":  0x8C7F,
	"MAX_TRANSFORM_FEEDBACK_SEPARATE_COMPONENTS":    0x8C80,
	"TRANSFORM_FEEDBACK_VARYINGS":                   0x8C83,
	"TRANSFORM_FEEDBACK_BUFFER_START":               0x8C84,
	"TRANSFORM_FEEDBACK_BUFFER_SIZE":                0x8C85,
	"TRANSFORM_FEEDBACK_PRIMITIVES_WRITTEN":         0x8C88,
	"RASTERIZER_DISCARD":                            0x8C89,
	"MAX_TRANSFORM_FEEDBACK_INTERLEAVED_COMPONENTS": 0x8C8A,
	"MAX_TRANSFORM_FEEDBACK_SEPARATE_ATTRIBS":       0x8C8B,
	"INTERLEAVED_ATTRIBS":                           0x8C8C,
	"SEPARATE_ATTRIBS":                              0x8C8D,
	"TRANSFORM_FEEDBACK_BUFFER":                     0x8C8E,
	"TRANSFORM_FEEDBACK_BUFFER_BINDING":             0x8C8F,
	"TRANSFORM_FEEDBACK":                            0x8E22,
	"TRANSFORM_FEEDBACK_PAUSED":                     0x8E23,
	"TRANSFORM_FEEDBACK_ACTIVE":                     0x8E24,
	"TRANSFORM_FEEDBACK_BINDING":                    0x8E25,
	"FRAMEBUFFER_ATTACHMENT_COLOR_ENCODING":         0x8210,
	"FRAMEBUFFER_ATTACHMENT_COMPONENT_TYPE":         0x8211,
	"FRAMEBUFFER_ATTACHMENT_RED_SIZE":               0x8212,
	"FRAMEBUFFER_ATTACHMENT_GREEN_SIZE":             0x8213,
	"FRAMEBUFFER_ATTACHMENT_BLUE_SIZE":              0x8214,
	"FRAMEBUFFER_ATTACHMENT_ALPHA_SIZE":             0x8215,
	"FRAMEBUFFER_ATTACHMENT_DEPTH_SIZE":             0x8216,
	"FRAMEBUFFER_ATTACHMENT_STENCIL_SIZE":           0x8217,
	"FRAMEBUFFER_DEFAULT":                           0x8218,
	"DEPTH24_STENCIL8":                              0x88F0,
	"UNSIGNED_NORMALIZED":                           0x8C17,
	"DRAW_FRAMEBUFFER_BINDING":                      0x8CA6,
	"READ_FRAMEBUFFER":                              0x8CA8,
	"DRAW_FRAMEBUFFER":                              0x8CA9,
	"READ_FRAMEBUFFER_BINDING":                      0x8CAA,
	"RENDERBUFFER_SAMPLES":                          0x8CAB,
	"FRAMEBUFFER_ATTACHMENT_TEXTURE_LAYER":          0x8CD4,
	"MAX_COLOR_ATTACHMENTS":                         0x8CDF,
	"COLOR_ATTACHMENT1":                             0x8CE1,
	"COLOR_ATTACHMENT2":                             0x8CE2,
	"COLOR_ATTACHMENT3":                             0x8CE3,
	"COLOR_ATTACHMENT4":                             0x8CE4,
	"COLOR_ATTACHMENT5":                             0x8CE5,
	"COLOR_ATTACHMENT6":                             0x8CE6,
	"COLOR_ATTACHMENT7":                             0x8CE7,
	"COLOR_ATTACHMENT8":                             0x8CE8,
	"COLOR_ATTACHMENT9":                             0x8CE9,
	"COLOR_ATTACHMENT10":                            0x8CEA,
	"COLOR_ATTACHMENT11":                            0x8CEB,
	"COLOR_ATTACHMENT12":                            0x8CEC,
	"COLOR_ATTACHMENT13":                            0x8CED,
	"COLOR_ATTACHMENT14":                            0x8CEE,
	"COLOR_ATTACHMENT15":                            0x8CEF,
	"FRAMEBUFFER_INCOMPLETE_MULTISAMPLE":            0x8D56,
	"MAX_SAMPLES":                                   0x8D57,
	"RG":                                            0x8227,
	"RG_INTEGER":                                    0x8228,
	"R8":                                            0x8229,
	"RG8":                                           0x822B,
	"R16F":                                          0x822D,
	"R32F":                                          0x822E,
	"RG16F":                                         0x822F,
	"RG32F":                                         0x8230,
	"R8I":                                           0x8231,
	"R8UI":                                          0x8232,
	"R16I":                                          0x8233,
	"R16UI":                                         0x8234,
	"R32I":                                          0x8235,
	"R32UI":                                         0x8236,
	"RG8I":                                          0x8237,
	"RG8UI":                                         0x8238,
	"RG16I":                                         0x8239,
	"RG16UI":                                        0x823A,
	"RG32I":                                         0x823B,
	"RG32UI":                                        0x823C,
	"VERTEX_ARRAY_BINDING":                          0x85B5,
	"R8_SNORM":                                      0x8F94,
	"RG8_SNORM":                                     0x8F95,
	"RGB8_SNORM":                                    0x8F96,
	"RGBA8_SNORM":                                   0x8F97,
	"SIGNED_NORMALIZED":                             0x8F9C,
	"COPY_READ_BUFFER":                              0x8F36,
	"COPY_WRITE_BUFFER":                             0x8F37,
	"COPY_READ_BUFFER_BINDING":                      0x8F36,
	"COPY_WRITE_BUFFER_BINDING":                     0x8F37,
	"UNIFORM_BUFFER":                                0x8A11,
	"UNIFORM_BUFFER_BINDING":                        0x8A28,
	"UNIFORM_BUFFER_START":                          0x8A29,
	"UNIFORM_BUFFER_SIZE":                           0x8A2A,
	"MAX_VERTEX_UNIFORM_BLOCKS":                     0x8A2B,
	"MAX_FRAGMENT_UNIFORM_BLOCKS":                   0x8A2D,
	"MAX_COMBINED_UNIFORM_BLOCKS":                   0x8A2E,
	"MAX_UNIFORM_BUFFER_BINDINGS":                   0x8A2F,
	"MAX_UNIFORM_BLOCK_SIZE":                        0x8A30,
	"MAX_COMBINED_VERTEX_UNIFORM_COMPONENTS":        0x8A31,
	"MAX_COMBINED_FRAGMENT_UNIFORM_COMPONENTS":      0x8A33,
	"UNIFORM_BUFFER_OFFSET_ALIGNMENT":               0x8A34,
	"ACTIVE_UNIFORM_BLOCKS":                         0x8A36,
	"UNIFORM_TYPE":                                  0x8A37,
	"UNIFORM_SIZE":                                  0x8A38,
	"UNIFORM_BLOCK_INDEX":                           0x8A3A,
	"UNIFORM_OFFSET":                                0x8A3B,
	"UNIFORM_ARRAY_STRIDE":                          0x8A3C,
	"UNIFORM_MATRIX_STRIDE":                         0x8A3D,
	"UNIFORM_IS_ROW_MAJOR":                          0x8A3E,
	"UNIFORM_BLOCK_BINDING":                         0x8A3F,
	"UNIFORM_BLOCK_DATA_SIZE":                       0x8A40,
	"UNIFORM_BLOCK_ACTIVE_UNIFORMS":                 0x8A42,
	"UNIFORM_BLOCK_ACTIVE_UNIFORM_INDICES":          0x8A43,
	"UNIFORM_BLOCK_REFERENCED_BY_VERTEX_SHADER":     0x8A44,
	"UNIFORM_BLOCK_REFERENCED_BY_FRAGMENT_SHADER":   0x8A46,
	"INVALID_INDEX":                                 0xFFFFFFFF,
	"MAX_VERTEX_OUTPUT_COMPONENTS":                  0x9122,
	"MAX_FRAGMENT_INPUT_COMPONENTS":                 0x9125,
	"MAX_SERVER_WAIT_TIMEOUT":                       0x9111,
	"OBJECT_TYPE":                                   0x9112,
	"SYNC_CONDITION":                                0x9113,
	"SYNC_STATUS":                                   0x9114,
	"SYNC_FLAGS":                                    0x9115,
	"SYNC_FENCE":                                    0x9116,
	"SYNC_GPU_COMMANDS_COMPLETE":                    0x9117,
	"UNSIGNALED":                                    0x9118,
	"SIGNALED":                                      0x9119,
	"ALREADY_SIGNALED":                              0x911A,
	"TIMEOUT_EXPIRED":                               0x911B,
	"CONDITION_SATISFIED":                           0x911C,
	"WAIT_FAILED":                                   0x911D,
	"SYNC_FLUSH_COMMANDS_BIT":                       0x1,
	"VERTEX_ATTRIB_ARRAY_DIVISOR":                   0x88FE,
	"ANY_SAMPLES_PASSED":                            0x8C2F,
	"ANY_SAMPLES_PASSED_CONSERVATIVE":               0x8D6A,
	"SAMPLER_BINDING":                               0x8919,
	"RGB10_A2UI":                                    0x906F,
	"INT_2_10_10_10_REV":                            0x8D9F,
	"RGBA32UI":                                      0x8D70,
	"RGB32UI":                                       0x8D71,
	"RGBA16UI":                                      0x8D76,
	"RGB16UI":                                       0x8D77,
	"RGBA8UI":                                       0x8D7C,
	"RGB8UI":                                        0x8D7D,
	"RGBA32I":                                       0x8D82,
	"RGB32I":                                        0x8D83,
	"RGBA16I":                                       0x8D88,
	"RGB16I":                                        0x8D89,
	"RGBA8I":                                        0x8D8E,
	"RGB8I":                                         0x8D8F,
	"RED_INTEGER":                                   0x8D94,
	"RGB_INTEGER":                                   0x8D98,
	"RGBA_INTEGER":                                  0x8D99,
	"SAMPLER_2D_ARRAY":                              0x8DC1,
	"SAMPLER_2D_ARRAY_SHADOW":                       0x8DC4,
	"SAMPLER_CUBE_SHADOW":                           0x8DC5,
	"UNSIGNED_INT_VEC2":                             0x8DC6,
	"UNSIGNED_INT_VEC3":                             0x8DC7,
	"UNSIGNED_INT_VEC4":                             0x8DC8,
	"INT_SAMPLER_2D":                                0x8DCA,
	"INT_SAMPLER_3D":                                0x8DCB,
	"INT_SAMPLER_CUBE":                              0x8DCC,
	"INT_SAMPLER_2D_ARRAY":                          0x8DCF,
	"UNSIGNED_INT_SAMPLER_2D":                       0x8DD2,
	"UNSIGNED_INT_SAMPLER_3D":                       0x8DD3,
	"UNSIGNED_INT_SAMPLER_CUBE":                     0x8DD4,
	"UNSIGNED_INT_SAMPLER_2D_ARRAY":                 0x8DD7,
	"DEPTH_COMPONENT32F":                            0x8CAC,
	"DEPTH32F_STENCIL8":                             0x8CAD,
	"FLOAT_32_UNSIGNED_INT_24_8_REV":                0x8DAD,
	"UNSIGNED_INT_24_8":                             0x84FA,
	"TEXTURE_IMMUTABLE_FORMAT":                      0x912F,
	"TEXTURE_IMMUTABLE_LEVELS":                      0x82DF,
	"MAX_CLIENT_WAIT_TIMEOUT_WEBGL":                 0x9247,
}

// ExtConstants lists constant names and values.
var ExtConstants = map[string]uint32{
	"HALF_FLOAT_OES":                            0x8D61,
	"DEPTH_STENCIL_UNSIGNED_INT_24_8_WEBGL":     0x84FA,
	"MAX_TEXTURE_MAX_ANISOTROPY_EXT":            0x84FF,
	"TEXTURE_MAX_ANISOTROPY_EXT":                0x84FE,
	"UNMASKED_VENDOR_WEBGL":                     0x9245,
	"UNMASKED_RENDERER_WEBGL":                   0x9246,
	"MAX_DRAW_BUFFERS_WEBGL":                    0x8824,
	"VERTEX_ARRAY_BINDING_OES":                  0x85B5,
	"VERTEX_ATTRIB_ARRAY_DIVISOR_ANGLE":         0x88FE,
	"FRAGMENT_SHADER_DERIVATIVE_HINT_OES":       0x8B8B,
	"MIN_EXT":                                   0x8007,
	"MAX_EXT":                                   0x8008,
	"SRGB_EXT":                                  0x8C40,
	"SRGB_ALPHA_EXT":                            0x8C42,
	"SRGB8_ALPHA8_EXT":                          0x8C43,
	"RGBA32F_EXT":                               0x8814,
	"RGB32F_EXT":                                0x8815,
	"RGBA16F_EXT":                               0x881A,
	"RGB16F_EXT":                                0x881B,
	"UNSIGNED_NORMALIZED_EXT":                   0x8C17,
	"FRAMEBUFFER_ATTACHMENT_COMPONENT_TYPE_EXT": 0x8211,
	"FRAMEBUFFER_ATTACHMENT_COLOR_ENCODING_EXT": 0x8210,
	"UNSIGNED_INT_24_8_WEBGL":                   0x84FA,
	"QUERY_COUNTER_BITS_EXT":                    0x8864,
	"TIME_ELAPSED_EXT":                          0x88BF,
	"TIMESTAMP_EXT":                             0x8E28,
	"GPU_DISJOINT_EXT":                          0x8FBB,
	"COMPLETION_STATUS_KHR":                     0x91B1,
}
