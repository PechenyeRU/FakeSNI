package main

import (
	"crypto/rand"
	"encoding/binary"
	mrand "math/rand"
	"sync"
)

// greaseValues are the 16 GREASE code points reserved by RFC 8701.
var greaseValues = []uint16{
	0x0A0A, 0x1A1A, 0x2A2A, 0x3A3A, 0x4A4A, 0x5A5A, 0x6A6A, 0x7A7A,
	0x8A8A, 0x9A9A, 0xAAAA, 0xBABA, 0xCACA, 0xDADA, 0xEAEA, 0xFAFA,
}

var greaseMu sync.Mutex
var greaseRand = mrand.New(mrand.NewSource(0))

func pickGrease() uint16 {
	greaseMu.Lock()
	defer greaseMu.Unlock()
	return greaseValues[greaseRand.Intn(len(greaseValues))]
}

func init() {
	var b [8]byte
	_, _ = rand.Read(b[:])
	seed := int64(binary.BigEndian.Uint64(b[:]))
	greaseMu.Lock()
	greaseRand = mrand.New(mrand.NewSource(seed))
	greaseMu.Unlock()
}

// u16 appends a big-endian uint16.
func u16(dst []byte, v uint16) []byte {
	return binary.BigEndian.AppendUint16(dst, v)
}

// lenPrefixed16 writes a 2-byte length followed by body.
func lenPrefixed16(body []byte) []byte {
	out := make([]byte, 0, 2+len(body))
	out = u16(out, uint16(len(body)))
	return append(out, body...)
}

// extension wraps body with a 2-byte type and 2-byte length.
func extension(extType uint16, body []byte) []byte {
	out := make([]byte, 0, 4+len(body))
	out = u16(out, extType)
	out = u16(out, uint16(len(body)))
	return append(out, body...)
}

// extServerName builds the SNI extension (type 0x0000).
func extServerName(host string) []byte {
	h := []byte(host)
	entry := make([]byte, 0, 3+len(h))
	entry = append(entry, 0x00) // host_name
	entry = u16(entry, uint16(len(h)))
	entry = append(entry, h...)
	return extension(0x0000, lenPrefixed16(entry))
}

// extSupportedGroups lists the named curves we accept for key exchange.
func extSupportedGroups(grease uint16) []byte {
	body := make([]byte, 0, 10)
	body = u16(body, grease)
	body = u16(body, 0x001d) // x25519
	body = u16(body, 0x0017) // secp256r1
	body = u16(body, 0x0018) // secp384r1
	return extension(0x000a, lenPrefixed16(body))
}

// extECPointFormats advertises uncompressed points.
func extECPointFormats() []byte {
	body := []byte{0x01, 0x00} // length=1, uncompressed
	return extension(0x000b, body)
}

// extSessionTicket: empty (no ticket to resume).
func extSessionTicket() []byte {
	return extension(0x0023, nil)
}

// extALPN advertises h2 and http/1.1 in that order.
func extALPN() []byte {
	body := []byte{}
	body = append(body, 0x02, 'h', '2')
	body = append(body, 0x08, 'h', 't', 't', 'p', '/', '1', '.', '1')
	return extension(0x0010, lenPrefixed16(body))
}

// extStatusRequest: OCSP status_request, empty responder list.
func extStatusRequest() []byte {
	body := []byte{0x01, 0x00, 0x00, 0x00, 0x00}
	return extension(0x0005, body)
}

// extSignatureAlgorithms lists Chrome's sigalgs in order.
func extSignatureAlgorithms() []byte {
	algs := []uint16{
		0x0403, // ecdsa_secp256r1_sha256
		0x0804, // rsa_pss_rsae_sha256
		0x0401, // rsa_pkcs1_sha256
		0x0503, // ecdsa_secp384r1_sha384
		0x0805, // rsa_pss_rsae_sha384
		0x0501, // rsa_pkcs1_sha384
		0x0806, // rsa_pss_rsae_sha512
		0x0601, // rsa_pkcs1_sha512
	}
	body := make([]byte, 0, 2+len(algs)*2)
	body = u16(body, uint16(len(algs)*2))
	for _, a := range algs {
		body = u16(body, a)
	}
	return extension(0x000d, body)
}

// extSignedCertTimestamp (SCT) request, empty body.
func extSignedCertTimestamp() []byte {
	return extension(0x0012, nil)
}

// extKeyShare ships a single X25519 share plus a GREASE share of 1 byte.
func extKeyShare(grease uint16) []byte {
	var pub [32]byte
	_, _ = rand.Read(pub[:])
	// X25519 public keys must have the top bit clear in the last byte (per RFC 7748
	// clamping of the private side). For fingerprint purposes we leave the raw 32 bytes;
	// this matches what Chrome puts on the wire.
	shares := []byte{}
	// GREASE entry with 1-byte placeholder.
	shares = u16(shares, grease)
	shares = u16(shares, 1)
	shares = append(shares, 0x00)
	// X25519 entry.
	shares = u16(shares, 0x001d)
	shares = u16(shares, 32)
	shares = append(shares, pub[:]...)
	return extension(0x0033, lenPrefixed16(shares))
}

// extPSKModes: psk_dhe_ke.
func extPSKModes() []byte {
	body := []byte{0x01, 0x01}
	return extension(0x002d, body)
}

// extSupportedVersions lists GREASE, TLS 1.3, TLS 1.2.
func extSupportedVersions(grease uint16) []byte {
	body := []byte{}
	body = append(body, 0x06)
	body = u16(body, grease)
	body = u16(body, 0x0304) // TLS 1.3
	body = u16(body, 0x0303) // TLS 1.2
	return extension(0x002b, body)
}

// extCompressCertificate: brotli.
func extCompressCertificate() []byte {
	body := []byte{0x02, 0x00, 0x02}
	return extension(0x001b, body)
}

// extApplicationSettings (ALPS): h2.
func extApplicationSettings() []byte {
	body := []byte{}
	body = append(body, 0x00, 0x03, 0x02, 'h', '2')
	return extension(0x4469, body)
}

// extExtendedMasterSecret: empty.
func extExtendedMasterSecret() []byte {
	return extension(0x0017, nil)
}

// extRenegotiationInfo: empty info (no renegotiation context).
func extRenegotiationInfo() []byte {
	body := []byte{0x00}
	return extension(0xff01, body)
}

// extGREASE0 is the leading GREASE extension with empty body.
func extGREASE0(grease uint16) []byte {
	return extension(grease, nil)
}

// extGREASELast is the trailing GREASE extension with a single zero byte.
func extGREASELast(grease uint16) []byte {
	return extension(grease, []byte{0x00})
}

// extPadding pads extensions so the whole record reaches targetLen.
func extPadding(currentLen, targetLen int) []byte {
	needed := targetLen - currentLen - 4 // 4 = ext header
	if needed < 0 {
		needed = 0
	}
	body := make([]byte, needed)
	return extension(0x0015, body)
}

// cipherSuites returns the Chrome TLS 1.3 + 1.2 cipher list with leading GREASE.
func cipherSuites(grease uint16) []byte {
	ids := []uint16{
		grease,
		0x1301, // TLS_AES_128_GCM_SHA256
		0x1302, // TLS_AES_256_GCM_SHA384
		0x1303, // TLS_CHACHA20_POLY1305_SHA256
		0xc02b, // ECDHE-ECDSA-AES128-GCM-SHA256
		0xc02f, // ECDHE-RSA-AES128-GCM-SHA256
		0xc02c, // ECDHE-ECDSA-AES256-GCM-SHA384
		0xc030, // ECDHE-RSA-AES256-GCM-SHA384
		0xcca9, // ECDHE-ECDSA-CHACHA20-POLY1305
		0xcca8, // ECDHE-RSA-CHACHA20-POLY1305
		0xc013, // ECDHE-RSA-AES128-SHA
		0xc014, // ECDHE-RSA-AES256-SHA
		0x009c, // AES128-GCM-SHA256
		0x009d, // AES256-GCM-SHA384
		0x002f, // AES128-SHA
		0x0035, // AES256-SHA
	}
	out := make([]byte, 0, len(ids)*2)
	for _, c := range ids {
		out = u16(out, c)
	}
	return out
}

// BuildClientHello produces a ClientHello record whose wire layout matches the
// JA3 fingerprint of Chrome 131+. The record is padded to the next multiple of
// 512 bytes so DPI can't match on a constant length.
func BuildClientHello(sni string) []byte {
	g0 := pickGrease()
	g1 := pickGrease()
	for g1 == g0 {
		g1 = pickGrease()
	}
	gGroups := pickGrease()
	gKeyShare := pickGrease()
	gVersions := pickGrease()

	var random [32]byte
	_, _ = rand.Read(random[:])
	var sessionID [32]byte
	_, _ = rand.Read(sessionID[:])

	extsCore := []byte{}
	extsCore = append(extsCore, extGREASE0(g0)...)
	extsCore = append(extsCore, extServerName(sni)...)
	extsCore = append(extsCore, extExtendedMasterSecret()...)
	extsCore = append(extsCore, extRenegotiationInfo()...)
	extsCore = append(extsCore, extSupportedGroups(gGroups)...)
	extsCore = append(extsCore, extECPointFormats()...)
	extsCore = append(extsCore, extSessionTicket()...)
	extsCore = append(extsCore, extALPN()...)
	extsCore = append(extsCore, extStatusRequest()...)
	extsCore = append(extsCore, extSignatureAlgorithms()...)
	extsCore = append(extsCore, extSignedCertTimestamp()...)
	extsCore = append(extsCore, extKeyShare(gKeyShare)...)
	extsCore = append(extsCore, extPSKModes()...)
	extsCore = append(extsCore, extSupportedVersions(gVersions)...)
	extsCore = append(extsCore, extCompressCertificate()...)
	extsCore = append(extsCore, extApplicationSettings()...)
	extsCore = append(extsCore, extGREASELast(g1)...)

	cs := cipherSuites(g0)
	bodyFixed := 2 + 32 + 1 + 32 + 2 + len(cs) + 2 + 2
	hsHdr := 4
	recHdr := 5

	currentExtsLen := len(extsCore)
	totalNoPad := recHdr + hsHdr + bodyFixed + currentExtsLen
	target := ((totalNoPad / 512) + 1) * 512
	pad := extPadding(totalNoPad, target)

	exts := make([]byte, 0, len(extsCore)+len(pad))
	exts = append(exts, extsCore...)
	exts = append(exts, pad...)

	body := make([]byte, 0, bodyFixed+len(exts))
	body = append(body, 0x03, 0x03)
	body = append(body, random[:]...)
	body = append(body, byte(len(sessionID)))
	body = append(body, sessionID[:]...)
	body = u16(body, uint16(len(cs)))
	body = append(body, cs...)
	body = append(body, 0x01, 0x00)
	body = u16(body, uint16(len(exts)))
	body = append(body, exts...)

	hs := make([]byte, 0, 4+len(body))
	hs = append(hs, 0x01)
	hs = append(hs, byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
	hs = append(hs, body...)

	rec := make([]byte, 0, 5+len(hs))
	rec = append(rec, 0x16)
	rec = append(rec, 0x03, 0x01)
	rec = u16(rec, uint16(len(hs)))
	rec = append(rec, hs...)
	return rec
}
