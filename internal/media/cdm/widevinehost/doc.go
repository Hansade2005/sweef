// Package widevinehost hosts a Chromium-ABI Content Decryption Module (the Widevine CDM shared library) from pure
// Go, without cgo. It exists for the separate helper binary cmd/sweef-cdm, which is allowed to be dynamically
// linked; the static Sweef browser never imports this package (the browser talks to the helper through
// internal/media/cdm's framed protocol).
//
// What it does, in the terms of Chromium's media/cdm/api/content_decryption_module.h (the CDM API; the copy this
// code was written against is the chromium/cdm repository, interface versions 10, 11 and 12, module version 4):
//
//   - loads the library (dlopen via purego on Linux and macOS, LoadLibrary on Windows) and resolves the exported C
//     functions InitializeCdmModule_4, DeinitializeCdmModule, CreateCdmInstance, GetCdmVersion and the optional
//     VerifyCdmHost_0 (content_decryption_module_ext.h);
//   - builds the C++ object the CDM expects as its host, cdm::Host_10/_11/_12, as a function-pointer table whose
//     entries are purego callbacks, and the helper objects the CDM calls back into (cdm::Buffer,
//     cdm::DecryptedBlock, cdm::FileIO);
//   - calls the CDM's cdm::ContentDecryptionModule_10/_11/_12 virtual methods through its vtable;
//   - turns the CDM's promise style API into the cdm.Engine interface.
//
// # The C++ ABI in two paragraphs
//
// A pure virtual C++ class without data members is, to its callers, a pointer to a pointer to an array of function
// pointers. Entry i of the array is the i-th virtual function in declaration order; the virtual destructor, which
// these classes declare last, comes after all of them and is never called through the table. The first argument of
// every method is the object pointer ("this"). That layout is the same for the Itanium C++ ABI (Linux, macOS) and
// for MSVC on 64-bit Windows. The classes here have a single virtual destructor and no overloaded virtuals, so
// MSVC's reordering of overloads does not apply.
//
// Arguments are passed as for a C function with the extra leading this: integers and pointers in integer
// registers, bool as its low byte, 8-byte structs such as cdm::Size by value in one integer register, larger
// structs (InputBuffer_2, Policy, PlatformChallengeResponse) by reference. A double (cdm::Time) is the one thing
// purego callbacks cannot return on every platform: on Linux and macOS a callback returns a one-field struct that
// the ABI places in xmm0/d0; on Windows syscall.NewCallback has no floating point at all, so a few bytes of
// machine code are generated (see walltime_windows.go).
//
// # Status of verification
//
// Nothing in this package has run against a real Widevine library: the sandbox this was written in has none, and
// none may be bundled. The struct sizes and vtable slot numbers are asserted at compile time and in the tests; a
// fake CDM written in Go that implements the same ABI through function-pointer tables exercises the whole host
// (callbacks, argument decoding, buffers, timers, file IO, promises) end to end. How the real library behaves
// (VMP handling, provisioning, threading) remains unverified; docs/drm.md lists it.
package widevinehost
