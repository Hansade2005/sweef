package engine

import "testing"

const cryptoHelpers = `
const hex = (b) => Array.from(new Uint8Array(b), x => x.toString(16).padStart(2, '0')).join('');
const unhex = (h) => Uint8Array.from(h.match(/../g) || [], x => parseInt(x, 16));
const S = crypto.subtle;
const b64u = (h) => btoa(String.fromCharCode(...unhex(h))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
`

// Known-answer tests from the RFCs / NIST documents, and the WebCrypto contract (names of errors, usages,
// extractability) as pages observe it. WhatsApp Web's Signal protocol needs X25519, Ed25519 (via curve25519 in WASM, but
// also native), HKDF, HMAC, AES-CBC / AES-GCM / AES-CTR and PBKDF2, and Google's apps use ECDH/ECDSA P-256 and AES-GCM.
func TestWebCryptoKnownAnswers(t *testing.T) {
	tab := platformTab(t)
	pre := cryptoHelpers
	runJSCases(t, tab, []jsCase{
		{"sha-256 abc", pre + `return hex(await S.digest('SHA-256', new TextEncoder().encode('abc')));`, `"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"`},
		{"sha-1 and 512 lengths", pre + `const d = new TextEncoder().encode('x'); return [(await S.digest('SHA-1', d)).byteLength, (await S.digest({ name: 'sha-384' }, d)).byteLength, (await S.digest('SHA-512', d)).byteLength];`, `[20,48,64]`},
		{"digest result is an ArrayBuffer", pre + `const r = await S.digest('SHA-256', new Uint8Array(0)); return [r instanceof ArrayBuffer, hex(r)];`, `[true,"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"]`},
		{"hmac rfc4231 case 2", pre + `const k = await S.importKey('raw', new TextEncoder().encode('Jefe'), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign', 'verify']); const m = new TextEncoder().encode('what do ya want for nothing?'); const sig = await S.sign('HMAC', k, m); return [hex(sig), await S.verify('HMAC', k, sig, m), await S.verify('HMAC', k, sig, new TextEncoder().encode('x'))];`, `["5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843",true,false]`},
		{"aes-gcm mcgrew case 2", pre + `const k = await S.importKey('raw', new Uint8Array(16), 'AES-GCM', false, ['encrypt', 'decrypt']); const ct = await S.encrypt({ name: 'AES-GCM', iv: new Uint8Array(12) }, k, new Uint8Array(16)); const pt = await S.decrypt({ name: 'AES-GCM', iv: new Uint8Array(12) }, k, ct); return [hex(ct), pt.byteLength];`, `["0388dace60b6a392f328c2b971b2fe78ab6e47d42cec13bdf53a67b21257bddf",16]`},
		{"aes-gcm tamper fails with OperationError", pre + `const k = await S.generateKey({ name: 'AES-GCM', length: 256 }, true, ['encrypt', 'decrypt']); const iv = crypto.getRandomValues(new Uint8Array(12)); const ct = new Uint8Array(await S.encrypt({ name: 'AES-GCM', iv, additionalData: new Uint8Array([1]) }, k, new Uint8Array([1, 2, 3]))); ct[0] ^= 1; try { await S.decrypt({ name: 'AES-GCM', iv, additionalData: new Uint8Array([1]) }, k, ct); } catch (e) { return e.name; }`, `"OperationError"`},
		{"aes-gcm tagLength 96", pre + `const k = await S.importKey('raw', new Uint8Array(16), 'AES-GCM', false, ['encrypt', 'decrypt']); const a = { name: 'AES-GCM', iv: new Uint8Array(12), tagLength: 96 }; const ct = await S.encrypt(a, k, new Uint8Array(16)); return [ct.byteLength, (await S.decrypt(a, k, ct)).byteLength];`, `[28,16]`},
		{"aes-cbc sp800-38a", pre + `const k = await S.importKey('raw', unhex('2b7e151628aed2a6abf7158809cf4f3c'), 'AES-CBC', false, ['encrypt', 'decrypt']); const iv = unhex('000102030405060708090a0b0c0d0e0f'); const ct = await S.encrypt({ name: 'AES-CBC', iv }, k, unhex('6bc1bee22e409f96e93d7e117393172a')); const back = await S.decrypt({ name: 'AES-CBC', iv }, k, ct); return [hex(ct).slice(0, 32), ct.byteLength, hex(back)];`, `["7649abac8119b246cee98e9b12e9197d",32,"6bc1bee22e409f96e93d7e117393172a"]`},
		{"aes-cbc bad padding", pre + `const k = await S.importKey('raw', new Uint8Array(16), 'AES-CBC', false, ['decrypt']); try { await S.decrypt({ name: 'AES-CBC', iv: new Uint8Array(16) }, k, new Uint8Array(16)); } catch (e) { return e.name; }`, `"OperationError"`},
		{"aes-ctr sp800-38a", pre + `const k = await S.importKey('raw', unhex('2b7e151628aed2a6abf7158809cf4f3c'), 'AES-CTR', false, ['encrypt']); const ct = await S.encrypt({ name: 'AES-CTR', counter: unhex('f0f1f2f3f4f5f6f7f8f9fafbfcfdfeff'), length: 64 }, k, unhex('6bc1bee22e409f96e93d7e117393172a')); return hex(ct);`, `"874d6191b620e3261bef6864990db6ce"`},
		{"aes-ctr counter wraps within length", pre + `const k = await S.importKey('raw', new Uint8Array(16), 'AES-CTR', false, ['encrypt']); const c = new Uint8Array(16); c[15] = 0xff; const a = hex(await S.encrypt({ name: 'AES-CTR', counter: c, length: 8 }, k, new Uint8Array(32))); const c0 = new Uint8Array(16); const b = hex(await S.encrypt({ name: 'AES-CTR', counter: c0, length: 8 }, k, new Uint8Array(16))); return a.slice(32) === b;`, `true`},
		{"aes-kw rfc3394", pre + `const kek = await S.importKey('raw', unhex('000102030405060708090a0b0c0d0e0f'), 'AES-KW', false, ['wrapKey', 'unwrapKey']); const key = await S.importKey('raw', unhex('00112233445566778899aabbccddeeff'), { name: 'AES-GCM' }, true, ['encrypt']); const w = await S.wrapKey('raw', key, kek, 'AES-KW'); const u = await S.unwrapKey('raw', w, kek, 'AES-KW', 'AES-GCM', true, ['encrypt']); return [hex(w), hex(await S.exportKey('raw', u))];`, `["1fa68b0a8112b447aef34bd8fb5a7b829d3e862371d2cfe5","00112233445566778899aabbccddeeff"]`},
		{"hkdf rfc5869 case 1", pre + `const ikm = await S.importKey('raw', unhex('0b'.repeat(22)), 'HKDF', false, ['deriveBits']); const bits = await S.deriveBits({ name: 'HKDF', hash: 'SHA-256', salt: unhex('000102030405060708090a0b0c'), info: unhex('f0f1f2f3f4f5f6f7f8f9') }, ikm, 42 * 8); return hex(bits);`, `"3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865"`},
		{"hkdf empty salt and info", pre + `const ikm = await S.importKey('raw', unhex('0b'.repeat(22)), 'HKDF', false, ['deriveBits']); return hex(await S.deriveBits({ name: 'HKDF', hash: 'SHA-256', salt: new Uint8Array(0), info: new Uint8Array(0) }, ikm, 42 * 8));`, `"8da4e775a563c18f715f802a063c5a31b8a11f5c5ee1879ec3454e5f3c738d2d9d201395faa4b61a96c8"`},
		{"pbkdf2 sha-256", pre + `const pw = await S.importKey('raw', new TextEncoder().encode('password'), 'PBKDF2', false, ['deriveBits', 'deriveKey']); const bits = await S.deriveBits({ name: 'PBKDF2', hash: 'SHA-256', salt: new TextEncoder().encode('salt'), iterations: 1 }, pw, 256); return hex(bits);`, `"120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"`},
		{"pbkdf2 deriveKey to aes-gcm", pre + `const pw = await S.importKey('raw', new TextEncoder().encode('password'), 'PBKDF2', false, ['deriveKey']); const k = await S.deriveKey({ name: 'PBKDF2', hash: 'SHA-256', salt: new Uint8Array(8), iterations: 10 }, pw, { name: 'AES-GCM', length: 256 }, true, ['encrypt', 'decrypt']); return [k.type, k.algorithm.name, k.algorithm.length, k.extractable, k.usages, (await S.exportKey('raw', k)).byteLength];`, `["secret","AES-GCM",256,true,["encrypt","decrypt"],32]`},
		{"hkdf and pbkdf2 keys cannot be extractable", pre + `try { await S.importKey('raw', new Uint8Array(4), 'HKDF', true, ['deriveBits']); } catch (e) { return e.name; }`, `"SyntaxError"`},
		{"x25519 rfc7748 via jwk and raw", pre + `
const a = await S.importKey('jwk', { kty: 'OKP', crv: 'X25519', d: b64u('77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a'), x: b64u('8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a') }, { name: 'X25519' }, false, ['deriveBits']);
const bob = await S.importKey('raw', unhex('de9edb7d7b7dc1b4d35b61c2ece435373f8343c85b78674dadfc7e146f882b4f'), { name: 'X25519' }, true, []);
const z = await S.deriveBits({ name: 'X25519', public: bob }, a, 256);
return [hex(z), a.type, a.algorithm.name, bob.type, hex(await S.exportKey('raw', bob))];`, `["4a5d9d5ba4ce2de1728e3bf480350f25e07e21c947d19e3376f09b3c1e161742","private","X25519","public","de9edb7d7b7dc1b4d35b61c2ece435373f8343c85b78674dadfc7e146f882b4f"]`},
		{"ed25519 rfc8032 test 1", pre + `
const k = await S.importKey('jwk', { kty: 'OKP', crv: 'Ed25519', d: b64u('9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60'), x: b64u('d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a') }, { name: 'Ed25519' }, true, ['sign']);
const sig = await S.sign('Ed25519', k, new Uint8Array(0));
const pub = await S.importKey('raw', unhex('d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a'), 'Ed25519', true, ['verify']);
return [hex(sig), await S.verify('Ed25519', pub, sig, new Uint8Array(0)), await S.verify('Ed25519', pub, sig, new Uint8Array([1]))];`, `["e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b",true,false]`},
		{"ed25519 and x25519 generateKey pairs", pre + `const e = await S.generateKey('Ed25519', true, ['sign', 'verify']); const x = await S.generateKey({ name: 'X25519' }, true, ['deriveBits']); return [e.privateKey.usages, e.publicKey.usages, x.privateKey.usages, x.publicKey.usages, (await S.exportKey('raw', e.publicKey)).byteLength, (await S.exportKey('pkcs8', x.privateKey)).byteLength];`, `[["sign"],["verify"],["deriveBits"],[],32,48]`},
	})
}

func TestWebCryptoAsymmetric(t *testing.T) {
	tab := platformTab(t)
	pre := cryptoHelpers
	runJSCases(t, tab, []jsCase{
		{"ecdsa p-256 sign verify", pre + `const kp = await S.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, true, ['sign', 'verify']); const m = new TextEncoder().encode('hello'); const sig = await S.sign({ name: 'ECDSA', hash: 'SHA-256' }, kp.privateKey, m); const ok = await S.verify({ name: 'ECDSA', hash: 'SHA-256' }, kp.publicKey, sig, m); const bad = await S.verify({ name: 'ECDSA', hash: 'SHA-256' }, kp.publicKey, sig, new TextEncoder().encode('hellO')); return [sig.byteLength, ok, bad, kp.publicKey.algorithm.namedCurve, kp.privateKey.type, kp.publicKey.extractable];`, `[64,true,false,"P-256","private",true]`},
		{"ecdsa p-384 and p-521 signature sizes", pre + `const out = []; for (const c of ['P-384', 'P-521']) { const kp = await S.generateKey({ name: 'ECDSA', namedCurve: c }, false, ['sign', 'verify']); out.push((await S.sign({ name: 'ECDSA', hash: 'SHA-384' }, kp.privateKey, new Uint8Array(3))).byteLength); } return out;`, `[96,132]`},
		{"ec key export formats round trip", pre + `
const kp = await S.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, true, ['sign', 'verify']);
const raw = await S.exportKey('raw', kp.publicKey), spki = await S.exportKey('spki', kp.publicKey), p8 = await S.exportKey('pkcs8', kp.privateKey);
const jwkPub = await S.exportKey('jwk', kp.publicKey), jwkPriv = await S.exportKey('jwk', kp.privateKey);
const pub2 = await S.importKey('raw', raw, { name: 'ECDSA', namedCurve: 'P-256' }, true, ['verify']);
const pub3 = await S.importKey('spki', spki, { name: 'ECDSA', namedCurve: 'P-256' }, true, ['verify']);
const priv2 = await S.importKey('pkcs8', p8, { name: 'ECDSA', namedCurve: 'P-256' }, true, ['sign']);
const priv3 = await S.importKey('jwk', jwkPriv, { name: 'ECDSA', namedCurve: 'P-256' }, true, ['sign']);
const m = new Uint8Array([7, 7]); const a = { name: 'ECDSA', hash: 'SHA-256' };
const sig = await S.sign(a, priv3, m);
return [raw.byteLength, new Uint8Array(raw)[0], spki.byteLength, jwkPub.kty, jwkPub.crv, jwkPub.d === undefined, typeof jwkPriv.d, jwkPriv.ext, jwkPriv.key_ops, await S.verify(a, pub2, sig, m), await S.verify(a, pub3, await S.sign(a, priv2, m), m)];`, `[65,4,91,"EC","P-256",true,"string",true,["sign"],true,true]`},
		{"ecdh p-256 agreement and deriveKey", pre + `
const p = { name: 'ECDH', namedCurve: 'P-256' };
const a = await S.generateKey(p, true, ['deriveKey', 'deriveBits']), b = await S.generateKey(p, true, ['deriveKey', 'deriveBits']);
const z1 = hex(await S.deriveBits({ name: 'ECDH', public: b.publicKey }, a.privateKey, 256)), z2 = hex(await S.deriveBits({ name: 'ECDH', public: a.publicKey }, b.privateKey, 256));
const k1 = await S.deriveKey({ name: 'ECDH', public: b.publicKey }, a.privateKey, { name: 'AES-GCM', length: 256 }, true, ['encrypt', 'decrypt']);
const k2 = await S.deriveKey({ name: 'ECDH', public: a.publicKey }, b.privateKey, { name: 'AES-GCM', length: 256 }, true, ['encrypt', 'decrypt']);
const iv = new Uint8Array(12); const ct = await S.encrypt({ name: 'AES-GCM', iv }, k1, new Uint8Array([1, 2, 3]));
const pt = new Uint8Array(await S.decrypt({ name: 'AES-GCM', iv }, k2, ct));
const full = await S.deriveBits({ name: 'ECDH', public: b.publicKey }, a.privateKey, null);
let tooLong; try { await S.deriveBits({ name: 'ECDH', public: b.publicKey }, a.privateKey, 264); } catch (e) { tooLong = e.name; }
return [z1 === z2, z1.length, Array.from(pt), full.byteLength, tooLong, a.publicKey.usages];`, `[true,64,[1,2,3],32,"OperationError",[]]`},
		{"ecdh curve mismatch", pre + `const a = await S.generateKey({ name: 'ECDH', namedCurve: 'P-256' }, true, ['deriveBits']), b = await S.generateKey({ name: 'ECDH', namedCurve: 'P-384' }, true, ['deriveBits']); try { await S.deriveBits({ name: 'ECDH', public: b.publicKey }, a.privateKey, 128); } catch (e) { return e.name; }`, `"InvalidAccessError"`},
		{"rsa-oaep encrypt decrypt and jwk", pre + `
const kp = await S.generateKey({ name: 'RSA-OAEP', modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: 'SHA-256' }, true, ['encrypt', 'decrypt']);
const ct = await S.encrypt({ name: 'RSA-OAEP', label: new Uint8Array([9]) }, kp.publicKey, new Uint8Array([1, 2, 3, 4]));
const pt = new Uint8Array(await S.decrypt({ name: 'RSA-OAEP', label: new Uint8Array([9]) }, kp.privateKey, ct));
const jwk = await S.exportKey('jwk', kp.privateKey);
const re = await S.importKey('jwk', jwk, { name: 'RSA-OAEP', hash: 'SHA-256' }, true, ['decrypt']);
const pt2 = new Uint8Array(await S.decrypt({ name: 'RSA-OAEP', label: new Uint8Array([9]) }, re, ct));
let wrongLabel; try { await S.decrypt({ name: 'RSA-OAEP' }, kp.privateKey, ct); } catch (e) { wrongLabel = e.name; }
return [ct.byteLength, Array.from(pt), Array.from(pt2), jwk.kty, jwk.alg, ['n', 'e', 'd', 'p', 'q', 'dp', 'dq', 'qi'].every(k => typeof jwk[k] === 'string'), kp.publicKey.algorithm.modulusLength, Array.from(kp.publicKey.algorithm.publicExponent), kp.publicKey.algorithm.hash.name, wrongLabel];`, `[256,[1,2,3,4],[1,2,3,4],"RSA","RSA-OAEP-256",true,2048,[1,0,1],"SHA-256","OperationError"]`},
		{"rsa signatures", pre + `
const m = new Uint8Array([1, 2, 3]);
const k1 = await S.generateKey({ name: 'RSASSA-PKCS1-v1_5', modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: 'SHA-256' }, true, ['sign', 'verify']);
const s1 = await S.sign('RSASSA-PKCS1-v1_5', k1.privateKey, m);
const k2 = await S.generateKey({ name: 'RSA-PSS', modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: 'SHA-256' }, true, ['sign', 'verify']);
const s2 = await S.sign({ name: 'RSA-PSS', saltLength: 32 }, k2.privateKey, m);
const spki = await S.exportKey('spki', k1.publicKey);
const imp = await S.importKey('spki', spki, { name: 'RSASSA-PKCS1-v1_5', hash: 'SHA-256' }, true, ['verify']);
return [s1.byteLength, await S.verify('RSASSA-PKCS1-v1_5', k1.publicKey, s1, m), await S.verify('RSASSA-PKCS1-v1_5', imp, s1, new Uint8Array(1)), await S.verify({ name: 'RSA-PSS', saltLength: 32 }, k2.publicKey, s2, m), imp.algorithm.modulusLength];`, `[256,true,false,true,2048]`},
	})
}

func TestWebCryptoContract(t *testing.T) {
	tab := platformTab(t)
	pre := cryptoHelpers
	runJSCases(t, tab, []jsCase{
		{"shape", pre + `return [crypto instanceof Crypto, S instanceof SubtleCrypto, String(S), typeof CryptoKey, Object.prototype.toString.call(crypto), S === crypto.subtle];`, `[true,true,"[object SubtleCrypto]","function","[object Crypto]",true]`},
		{"CryptoKey cannot be constructed", pre + `try { new CryptoKey(); } catch (e) { return e.name; }`, `"TypeError"`},
		{"CryptoKey fields", pre + `const k = await S.generateKey({ name: 'HMAC', hash: 'SHA-256' }, false, ['verify', 'sign']); return [k instanceof CryptoKey, k.type, k.extractable, k.usages, k.algorithm.name, k.algorithm.hash.name, k.algorithm.length, String(k)];`, `[true,"secret",false,["sign","verify"],"HMAC","SHA-256",512,"[object CryptoKey]"]`},
		{"non-extractable key refuses export", pre + `const k = await S.generateKey({ name: 'AES-GCM', length: 128 }, false, ['encrypt']); try { await S.exportKey('raw', k); } catch (e) { return e.name; }`, `"InvalidAccessError"`},
		{"missing usage refuses operation", pre + `const k = await S.generateKey({ name: 'AES-GCM', length: 128 }, true, ['decrypt']); try { await S.encrypt({ name: 'AES-GCM', iv: new Uint8Array(12) }, k, new Uint8Array(1)); } catch (e) { return e.name; }`, `"InvalidAccessError"`},
		{"algorithm mismatch refuses operation", pre + `const k = await S.generateKey({ name: 'AES-CBC', length: 128 }, true, ['encrypt']); try { await S.encrypt({ name: 'AES-GCM', iv: new Uint8Array(12) }, k, new Uint8Array(1)); } catch (e) { return e.name; }`, `"InvalidAccessError"`},
		{"unknown algorithm", pre + `const out = []; for (const f of [() => S.digest('MD5', new Uint8Array(1)), () => S.generateKey('AES-XYZ', true, ['encrypt']), () => S.importKey('raw', new Uint8Array(16), 'RC4', true, ['encrypt'])]) { try { await f(); out.push('ok'); } catch (e) { out.push(e.name); } } return out;`, `["NotSupportedError","NotSupportedError","NotSupportedError"]`},
		{"bad usages and lengths", pre + `const out = []; for (const f of [() => S.generateKey({ name: 'AES-GCM', length: 128 }, true, ['sign']), () => S.generateKey({ name: 'AES-GCM', length: 128 }, true, []), () => S.generateKey({ name: 'AES-GCM', length: 100 }, true, ['encrypt']), () => S.importKey('raw', new Uint8Array(15), 'AES-GCM', true, ['encrypt']), () => S.generateKey({ name: 'AES-GCM', length: 128 }, true, ['bogus'])]) { try { await f(); out.push('ok'); } catch (e) { out.push(e.name); } } return out;`, `["SyntaxError","SyntaxError","OperationError","DataError","TypeError"]`},
		{"jwk oct import export", pre + `const k = await S.importKey('jwk', { kty: 'oct', k: 'AAECAwQFBgcICQoLDA0ODw', alg: 'A128GCM', ext: true }, 'AES-GCM', true, ['encrypt', 'decrypt']); const j = await S.exportKey('jwk', k); let bad; try { await S.importKey('jwk', { kty: 'oct', k: 'AAECAwQFBgcICQoLDA0ODw', alg: 'A256GCM' }, 'AES-GCM', true, ['encrypt']); } catch (e) { bad = e.name; } return [j.kty, j.k, j.alg, j.ext, j.key_ops, hex(await S.exportKey('raw', k)), bad];`, `["oct","AAECAwQFBgcICQoLDA0ODw","A128GCM",true,["encrypt","decrypt"],"000102030405060708090a0b0c0d0e0f","DataError"]`},
		{"jwk ext=false with extractable=true", pre + `try { await S.importKey('jwk', { kty: 'oct', k: 'AAECAwQFBgcICQoLDA0ODw', ext: false }, 'AES-GCM', true, ['encrypt']); } catch (e) { return e.name; }`, `"DataError"`},
		{"wrapKey unwrapKey jwk with aes-gcm", pre + `const kek = await S.generateKey({ name: 'AES-GCM', length: 256 }, true, ['wrapKey', 'unwrapKey']); const key = await S.generateKey({ name: 'HMAC', hash: 'SHA-256' }, true, ['sign']); const iv = new Uint8Array(12); const w = await S.wrapKey('jwk', key, kek, { name: 'AES-GCM', iv }); const u = await S.unwrapKey('jwk', w, kek, { name: 'AES-GCM', iv }, { name: 'HMAC', hash: 'SHA-256' }, true, ['sign']); return [hex(await S.exportKey('raw', u)) === hex(await S.exportKey('raw', key)), u.algorithm.name];`, `[true,"HMAC"]`},
		{"arguments are checked", pre + `const out = []; for (const f of [() => S.digest('SHA-256'), () => S.digest('SHA-256', 'text'), () => S.encrypt({ name: 'AES-GCM', iv: new Uint8Array(12) }, {}, new Uint8Array(1))]) { try { await f(); out.push('ok'); } catch (e) { out.push(e.name); } } return out;`, `["TypeError","TypeError","TypeError"]`},
		{"digest copies its input", pre + `const d = new Uint8Array([1, 2, 3]); const p = S.digest('SHA-256', d); d[0] = 99; const a = hex(await p); const b = hex(await S.digest('SHA-256', new Uint8Array([1, 2, 3]))); return a === b;`, `true`},
		{"getRandomValues", pre + `const a = crypto.getRandomValues(new Uint8Array(32)), b = crypto.getRandomValues(new Uint8Array(32)); const out = [a.some(x => x !== 0), hex(a) !== hex(b), crypto.getRandomValues(new Uint32Array(4)).length]; for (const f of [() => crypto.getRandomValues(new Float32Array(2)), () => crypto.getRandomValues(new Uint8Array(65537)), () => crypto.getRandomValues([1]), () => crypto.getRandomValues(new DataView(new ArrayBuffer(4)))]) { try { f(); out.push('ok'); } catch (e) { out.push(e.name); } } return out;`, `[true,true,4,"TypeMismatchError","QuotaExceededError","TypeMismatchError","TypeMismatchError"]`},
		{"getRandomValues fills a view at an offset", pre + `const buf = new Uint8Array(16); crypto.getRandomValues(buf.subarray(4, 12)); return [buf.slice(0, 4).every(x => x === 0), buf.slice(12).every(x => x === 0), buf.slice(4, 12).some(x => x !== 0)];`, `[true,true,true]`},
		{"randomUUID", pre + `const u = crypto.randomUUID(); return [/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(u), u !== crypto.randomUUID()];`, `[true,true]`},
	})
}
