//go:build amd64 || arm64

package widevinehost

import "unsafe"

// Everything here mirrors chromium/cdm content_decryption_module.h. Sizes are the 64-bit column of the header's
// CHECK_TYPE(type, size_32, size_64) lines; this package only supports 64-bit processes.

// cdm::Status
const (
	statusSuccess                = 0
	statusNeedMoreData           = 1
	statusNoKey                  = 2
	statusInitializationError    = 3
	statusDecryptError           = 4
	statusDecodeError            = 5
	statusDeferredInitialization = 6
)

// cdm::Exception
const (
	exTypeError         = 0
	exNotSupportedError = 1
	exInvalidStateError = 2
	exQuotaExceeded     = 3
)

// cdm::EncryptionScheme (uint32)
const (
	schemeUnencrypted = 0
	schemeCenc        = 1
	schemeCbcs        = 2
)

// cdm::InitDataType (uint32)
const (
	initCenc   = 0
	initKeyIDs = 1
	initWebM   = 2
)

// cdm::SessionType (uint32)
const (
	sessionTemporary         = 0
	sessionPersistentLicense = 1
)

// cdm::MessageType (uint32)
const (
	msgLicenseRequest       = 0
	msgLicenseRenewal       = 1
	msgLicenseRelease       = 2
	msgIndividualizationReq = 3
)

const (
	queryResultSucceeded = 0 // cdm::QueryResult
	queryResultFailed    = 1
	linkTypeUnknown      = 1 << 0 // cdm::OutputLinkTypes
	protectionNone       = 0      // cdm::OutputProtectionMethods
	fileIOStatusSuccess  = 0      // cdm::FileIOClient::Status
	fileIOStatusInUse    = 1
	fileIOStatusError    = 2
)

// cdm::KeyStatus / cdm::KeyStatus_2 (uint32)
const (
	keyUsable           = 0
	keyInternalError    = 1
	keyExpired          = 2
	keyOutputRestricted = 3
	keyOutputDownscaled = 4
	keyStatusPending    = 5
	keyReleased         = 6
	keyUsableInFuture   = 7 // KeyStatus_2 only (CDM_12)
)

// inputBuffer2 is cdm::InputBuffer_2: 80 bytes. The header declares the unnamed 32-bit padding fields so that the
// struct is the same size as the sum of its parts on 32-bit and 64-bit builds.
//
//	offset size field
//	     0    8 data            const uint8_t*
//	     8    4 data_size       uint32_t
//	    12    4 encryption_scheme (cdm::EncryptionScheme)
//	    16    8 key_id          const uint8_t*
//	    24    4 key_id_size
//	    28    4 (padding)
//	    32    8 iv              const uint8_t*
//	    40    4 iv_size
//	    44    4 (padding)
//	    48    8 subsamples      const cdm::SubsampleEntry*
//	    56    4 num_subsamples
//	    60    4 (padding)
//	    64    8 pattern         {uint32 crypt_byte_block; uint32 skip_byte_block}
//	    72    8 timestamp       int64_t, microseconds
type inputBuffer2 struct {
	data          uintptr
	dataSize      uint32
	scheme        uint32
	keyID         uintptr
	keyIDSize     uint32
	_             uint32
	iv            uintptr
	ivSize        uint32
	_             uint32
	subsamples    uintptr
	numSubsamples uint32
	_             uint32
	cryptBlocks   uint32
	skipBlocks    uint32
	timestamp     int64
}

// subsampleEntry is cdm::SubsampleEntry: 8 bytes {uint32 clear_bytes; uint32 cipher_bytes}.
type subsampleEntry struct {
	clearBytes  uint32
	cipherBytes uint32
}

// keyInformation is cdm::KeyInformation and cdm::KeyInformation_2: 24 bytes.
//
//	0 key_id (const uint8_t*), 8 key_id_size, 12 status, 16 system_code, 20 (tail padding)
type keyInformation struct {
	keyID      uintptr
	keyIDSize  uint32
	status     uint32
	systemCode uint32
	_          uint32
}

// platformChallengeResponse is cdm::PlatformChallengeResponse: 48 bytes; all zero reports a failed challenge.
type platformChallengeResponse struct {
	signedData          uintptr
	signedDataLength    uint32
	_                   uint32
	signature           uintptr
	signatureLength     uint32
	_                   uint32
	platformCertificate uintptr
	certificateLength   uint32
	_                   uint32
}

// audioDecoderConfig2 is cdm::AudioDecoderConfig_2: 32 bytes. Not used by the decrypt-only engine; pinned so a
// decode-capable engine can use it.
type audioDecoderConfig2 struct {
	codec           uint32
	channelCount    int32
	bitsPerChannel  int32
	samplesPerSec   int32
	extraData       uintptr
	extraDataSize   uint32
	encryptionSchem uint32
}

// videoDecoderConfig2 is cdm::VideoDecoderConfig_2 (CDM_10, CDM_11): 40 bytes.
type videoDecoderConfig2 struct {
	codec         uint32
	profile       uint32
	format        uint32
	_             uint32
	width, height int32
	extraData     uintptr
	extraDataSize uint32
	scheme        uint32
}

// videoDecoderConfig3 is cdm::VideoDecoderConfig_3 (CDM_12): 40 bytes; color_space {primary, transfer, matrix,
// range} replaces the padding.
type videoDecoderConfig3 struct {
	codec         uint32
	profile       uint32
	format        uint32
	colorPrimary  uint8
	colorTransfer uint8
	colorMatrix   uint8
	colorRange    uint8
	width, height int32
	extraData     uintptr
	extraDataSize uint32
	scheme        uint32
}

// Compile-time size checks: a negative constant is a build error.
const (
	_ = uint(unsafe.Sizeof(inputBuffer2{}) - 80)
	_ = uint(80 - unsafe.Sizeof(inputBuffer2{}))
	_ = uint(unsafe.Sizeof(subsampleEntry{}) - 8)
	_ = uint(8 - unsafe.Sizeof(subsampleEntry{}))
	_ = uint(unsafe.Sizeof(keyInformation{}) - 24)
	_ = uint(24 - unsafe.Sizeof(keyInformation{}))
	_ = uint(unsafe.Sizeof(platformChallengeResponse{}) - 48)
	_ = uint(48 - unsafe.Sizeof(platformChallengeResponse{}))
	_ = uint(unsafe.Sizeof(audioDecoderConfig2{}) - 32)
	_ = uint(32 - unsafe.Sizeof(audioDecoderConfig2{}))
	_ = uint(unsafe.Sizeof(videoDecoderConfig2{}) - 40)
	_ = uint(40 - unsafe.Sizeof(videoDecoderConfig2{}))
	_ = uint(unsafe.Sizeof(videoDecoderConfig3{}) - 40)
	_ = uint(40 - unsafe.Sizeof(videoDecoderConfig3{}))
)

// Virtual function slots of cdm::ContentDecryptionModule_10, _11 and _12 (declaration order; _12 changes the
// argument types of slots 11 and 14 but not their positions). Slot 20 and later are the destructors.
const (
	cdmInitialize                    = 0  // (bool allow_distinctive_identifier, bool allow_persistent_state, bool use_hw_secure_codecs)
	cdmGetStatusForPolicy            = 1  // (uint32 promise_id, const Policy&)
	cdmSetServerCertificate          = 2  // (uint32 promise_id, const uint8_t* cert, uint32 size)
	cdmCreateSessionAndGenerateReq   = 3  // (uint32 promise_id, SessionType, InitDataType, const uint8_t* data, uint32 size)
	cdmLoadSession                   = 4  // (uint32 promise_id, SessionType, const char* id, uint32 id_size)
	cdmUpdateSession                 = 5  // (uint32 promise_id, const char* id, uint32 id_size, const uint8_t* response, uint32 size)
	cdmCloseSession                  = 6  // (uint32 promise_id, const char* id, uint32 id_size)
	cdmRemoveSession                 = 7  // (uint32 promise_id, const char* id, uint32 id_size)
	cdmTimerExpired                  = 8  // (void* context)
	cdmDecrypt                       = 9  // (const InputBuffer_2&, DecryptedBlock*) -> Status
	cdmInitializeAudioDecoder        = 10 // (const AudioDecoderConfig_2&) -> Status
	cdmInitializeVideoDecoder        = 11 // (const VideoDecoderConfig_2& | _3&) -> Status
	cdmDeinitializeDecoder           = 12 // (StreamType)
	cdmResetDecoder                  = 13 // (StreamType)
	cdmDecryptAndDecodeFrame         = 14 // (const InputBuffer_2&, VideoFrame* | VideoFrame_2*) -> Status
	cdmDecryptAndDecodeSamples       = 15 // (const InputBuffer_2&, AudioFrames*) -> Status
	cdmOnPlatformChallengeResponse   = 16 // (const PlatformChallengeResponse&)
	cdmOnQueryOutputProtectionStatus = 17 // (QueryResult, uint32 link_mask, uint32 output_protection_mask)
	cdmOnStorageID                   = 18 // (uint32 version, const uint8_t* id, uint32 size)
	cdmDestroy                       = 19 // ()
	cdmSlots                         = 20
)

// Virtual function slots of cdm::Host_10 (0..17), Host_11 and Host_12 (adds slot 18).
const (
	hostAllocate                  = 0  // (uint32 capacity) -> Buffer*
	hostSetTimer                  = 1  // (int64 delay_ms, void* context)
	hostGetCurrentWallTime        = 2  // () -> double (cdm::Time, seconds since the Unix epoch)
	hostOnInitialized             = 3  // (bool success)
	hostOnResolveKeyStatusPromise = 4  // (uint32 promise_id, KeyStatus | KeyStatus_2)
	hostOnResolveNewSession       = 5  // (uint32 promise_id, const char* session_id, uint32 size)
	hostOnResolvePromise          = 6  // (uint32 promise_id)
	hostOnRejectPromise           = 7  // (uint32 promise_id, Exception, uint32 system_code, const char* msg, uint32 size)
	hostOnSessionMessage          = 8  // (const char* id, uint32 id_size, MessageType, const char* msg, uint32 size)
	hostOnSessionKeysChange       = 9  // (const char* id, uint32 id_size, bool has_additional_usable_key, const KeyInformation*, uint32 count)
	hostOnExpirationChange        = 10 // (const char* id, uint32 id_size, double new_expiry_time)
	hostOnSessionClosed           = 11 // (const char* id, uint32 id_size)
	hostSendPlatformChallenge     = 12 // (const char* service, uint32 size, const char* challenge, uint32 size)
	hostEnableOutputProtection    = 13 // (uint32 mask)
	hostQueryOutputProtection     = 14 // ()
	hostOnDeferredInitDone        = 15 // (StreamType, Status)
	hostCreateFileIO              = 16 // (FileIOClient*) -> FileIO*
	hostRequestStorageID          = 17 // (uint32 version)
	hostReportMetrics             = 18 // (MetricName, uint64 value)  [Host_11 and later]
	hostSlots                     = 19
)

// cdm::Buffer slots (host implements): the CDM calls these on buffers that Host::Allocate returned.
const (
	bufDestroy  = 0 // ()
	bufCapacity = 1 // () const -> uint32
	bufData     = 2 // () -> uint8_t*
	bufSetSize  = 3 // (uint32)
	bufSize     = 4 // () const -> uint32
	bufSlots    = 5
)

// cdm::DecryptedBlock slots (host implements).
const (
	blockSetDecryptedBuffer = 0 // (Buffer*)
	blockDecryptedBuffer    = 1 // () -> Buffer*
	blockSetTimestamp       = 2 // (int64)
	blockTimestamp          = 3 // () const -> int64
	blockSlots              = 4
)

// cdm::FileIO slots (host implements), cdm::FileIOClient slots (CDM implements).
const (
	fileOpen  = 0 // (const char* name, uint32 size)
	fileRead  = 1 // ()
	fileWrite = 2 // (const uint8_t* data, uint32 size)
	fileClose = 3 // ()
	fileSlots = 4

	clientOnOpenComplete  = 0 // (Status)
	clientOnReadComplete  = 1 // (Status, const uint8_t* data, uint32 size)
	clientOnWriteComplete = 2 // (Status)
)
