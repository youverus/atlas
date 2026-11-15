package fs

// imgSniffBytes is how much of a file is inspected to identify an image. 4100
// bytes covers the signature plus the PNG chunk headers needed to reject
// animated PNGs, which providers do not accept as inline images.
const imgSniffBytes = 4100

var pngSignature = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// ImageMimeType reports the image type providers accept for these bytes, or ""
// for anything else.
//
// It is exported for the same reason ResolvePath is: the front end decides which
// bytes become an inline image and which become text, and it has to decide the
// way the read tool does. There is one sniff in this tree, and this is it.
func ImageMimeType(data []byte) string {
	return detectImageMimeType(headOf(data))
}

// detectImageMimeType sniffs the leading bytes of a file and returns a supported
// image MIME type, or "" for anything else.
//
// The sniff matters because extensions lie: a ".png" holding an APNG or a
// truncated file must not be sent to a provider as an inline image.
func detectImageMimeType(data []byte) string {
	switch {
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		// FF D8 FF F7 marks a JPEG lossless variant providers reject.
		if len(data) >= 4 && data[3] == 0xf7 {
			return ""
		}
		return "image/jpeg"
	case startsWith(pngSignature, data):
		if isPNG(data) && !isAnimatedPNG(data) {
			return "image/png"
		}
		return ""
	case startsWithASCII("GIF87a", data, 0) || startsWithASCII("GIF89a", data, 0):
		return "image/gif"
	case startsWithASCII("RIFF", data, 0) && startsWithASCII("WEBP", data, 8):
		return "image/webp"
	case startsWithASCII("BM", data, 0) && isBMP(data):
		return "image/bmp"
	}
	return ""
}

// isPNG reports whether the PNG signature is followed by a valid IHDR chunk.
func isPNG(data []byte) bool {
	// A valid PNG carries an IHDR as its first chunk, 13 bytes long.
	return len(data) >= 16 && readU32BE(data, len(pngSignature)) == 13 && startsWithASCII("IHDR", data, 12)
}

// isAnimatedPNG scans the chunk list for an acTL (animation control) chunk that
// appears before the first IDAT.
func isAnimatedPNG(data []byte) bool {
	offset := len(pngSignature)
	for offset+8 <= len(data) {
		chunkLength := int(readU32BE(data, offset))
		chunkTypeOffset := offset + 4
		if startsWithASCII("acTL", data, chunkTypeOffset) {
			return true
		}
		if startsWithASCII("IDAT", data, chunkTypeOffset) {
			return false
		}
		next := offset + 8 + chunkLength + 4
		if next <= offset || next > len(data) {
			return false
		}
		offset = next
	}
	return false
}

// isBMP validates the header fields providers require rather than trusting the
// "BM" prefix, which any text file can start with.
func isBMP(data []byte) bool {
	if len(data) < 26 {
		return false
	}
	declaredFileSize := readU32LE(data, 2)
	pixelDataOffset := readU32LE(data, 10)
	dibHeaderSize := readU32LE(data, 14)
	if declaredFileSize != 0 && declaredFileSize < 26 {
		return false
	}
	if pixelDataOffset < 14+dibHeaderSize {
		return false
	}
	if declaredFileSize != 0 && pixelDataOffset >= declaredFileSize {
		return false
	}

	var colorPlanes, bitsPerPixel uint32
	switch {
	case dibHeaderSize == 12:
		colorPlanes = readU16LE(data, 22)
		bitsPerPixel = readU16LE(data, 24)
	case dibHeaderSize >= 40 && dibHeaderSize <= 124:
		if len(data) < 30 {
			return false
		}
		colorPlanes = readU16LE(data, 26)
		bitsPerPixel = readU16LE(data, 28)
	default:
		return false
	}

	switch bitsPerPixel {
	case 1, 4, 8, 16, 24, 32:
		return colorPlanes == 1
	}
	return false
}

func startsWith(prefix, data []byte) bool {
	if len(data) < len(prefix) {
		return false
	}
	for i, b := range prefix {
		if data[i] != b {
			return false
		}
	}
	return true
}

func startsWithASCII(text string, data []byte, offset int) bool {
	if len(data) < offset+len(text) {
		return false
	}
	for i := 0; i < len(text); i++ {
		if data[offset+i] != text[i] {
			return false
		}
	}
	return true
}

func readU16LE(data []byte, offset int) uint32 {
	return uint32(data[offset]) | uint32(data[offset+1])<<8
}

func readU32BE(data []byte, offset int) uint32 {
	return uint32(data[offset])<<24 | uint32(data[offset+1])<<16 | uint32(data[offset+2])<<8 | uint32(data[offset+3])
}

func readU32LE(data []byte, offset int) uint32 {
	return uint32(data[offset]) | uint32(data[offset+1])<<8 | uint32(data[offset+2])<<16 | uint32(data[offset+3])<<24
}
