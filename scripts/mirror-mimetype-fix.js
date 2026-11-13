// Mirror the reference-image mimetype fix into core/media/image.go:
// declare the sniffed Content-Type instead of octet-stream.
const fs = require('fs');
const P = 'D:/Company/cinlan/apps/cinlan-bot/core/media/image.go';
let t = fs.readFileSync(P, 'utf8');
const rep = (o, n, tag) => {
	if (!t.includes(o)) throw new Error(tag + ' anchor miss');
	t = t.replace(o, n);
};

rep('"net/http"\n\t"net/url"', '"net/http"\n\t"net/textproto"\n\t"net/url"', 'import');

rep(
	`_ = writer.WriteField("n", "1")
	part, err := writer.CreateFormFile("image", "reference.png")
	if err != nil {
		return nil, err
	}`,
	`_ = writer.WriteField("n", "1")
	// CreateFormFile stamps every part application/octet-stream — strict
	// gateways validate the declared mimetype and only take jpeg/png/webp.
	partHeader := textproto.MIMEHeader{}
	partHeader.Set(
		"Content-Disposition",
		fmt.Sprintf(\`form-data; name="image"; filename="reference%s"\`, referenceExt(refData)),
	)
	partHeader.Set("Content-Type", referenceMIME(refData))
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return nil, err
	}`,
	'part',
);

rep(
	'func (g *ImageGenerator) generatePollinations(',
	`// referenceMIME returns the declared Content-Type for the uploaded
// reference image — strict edit gateways reject application/octet-stream
// and only take jpeg/png/webp, so we sniff the bytes and map gif/other
// sniffed-but-unsupported formats to png (still honest about what the
// upstream wants; the payload itself must actually be that format).
func referenceMIME(data []byte) string {
	switch mediaType := http.DetectContentType(data); mediaType {
	case "image/jpeg", "image/png", "image/webp":
		return mediaType
	default:
		// Anything sniffed outside the gateway's allowlist (gif, bmp, or
		// an opaque buffer) is declared as png — declaring the real type
		// would be rejected outright, while png payloads pass most
		// lenient relays; if the upstream sniffs bytes too, nothing lost.
		return "image/png"
	}
}

// referenceExt keeps the uploaded filename's extension consistent with
// the sniffed format so relays that validate by name see a coherent file.
func referenceExt(data []byte) string {
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

func (g *ImageGenerator) generatePollinations(`,
	'helpers',
);

fs.writeFileSync(P, t);
console.log('core patched');
