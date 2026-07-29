package printer

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	"image/png"
	"math"
	"os"
	"strings"
	"sync"
	"unicode"

	xfont "golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	printerWidthDots       = 576
	printerDPI             = 203
	printerMarginXDots     = 12
	printerMarginYDots     = 12
	printerContentWidth    = printerWidthDots - 2*printerMarginXDots
	printerMaxHeightDots   = 3200
	printerHeaderHeight    = 46
	printerSummaryHeight   = 38
	printerSectionHeight   = 31
	printerBodyLineHeight  = 29
	printerSmallLineHeight = 23
)

var printerFontCandidates = []string{
	"/System/Library/Fonts/STHeiti Medium.ttc",
	"/Library/Fonts/Arial Unicode.ttf",
	"/System/Library/Fonts/STHeiti Light.ttc",
}

type printerFontSource struct {
	regular *opentype.Font
	bold    *opentype.Font
	err     error
}

type printerFaces struct {
	title    xfont.Face
	status   xfont.Face
	section  xfont.Face
	body     xfont.Face
	compact  xfont.Face
	summary  xfont.Face
	metadata xfont.Face
}

type rasterDocument struct {
	WidthDots   int
	HeightDots  int
	Media       string
	ContentType string
}

type receiptField struct {
	Key   string
	Value string
}

type rasterReceipt struct {
	Title        string
	Status       string
	Kind         string
	GPU          string
	Timestamp    string
	Warning      string
	RunFields    []receiptField
	ResultFields []receiptField
	Params       []receiptField
	Command      []string
	RunID        string
}

var cachedPrinterFonts sync.Once
var cachedPrinterFontSource printerFontSource

func renderReceiptPNG(receipt string) ([]byte, rasterDocument, error) {
	bitmap, document, err := renderReceiptBitmap(receipt)
	if err != nil {
		return nil, rasterDocument{}, err
	}
	var output bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&output, bitmap); err != nil {
		return nil, rasterDocument{}, fmt.Errorf("encode receipt PNG: %w", err)
	}
	document.ContentType = "image/png"
	return output.Bytes(), document, nil
}

func renderReceiptCUPSRaster(receipt string) ([]byte, rasterDocument, error) {
	bitmap, document, err := renderReceiptBitmap(receipt)
	if err != nil {
		return nil, rasterDocument{}, err
	}
	document.Media = fmt.Sprintf(
		"Custom.72x%.3fmm",
		float64(document.HeightDots)*25.4/printerDPI,
	)
	payload, err := encodeNativeCUPSRaster(bitmap, document.Media)
	if err != nil {
		return nil, rasterDocument{}, fmt.Errorf("encode native CUPS raster: %w", err)
	}
	document.ContentType = "application/vnd.cups-raster"
	return payload, document, nil
}

func renderReceiptBitmap(receipt string) (*image.Gray, rasterDocument, error) {
	fonts, closeFonts, err := newPrinterFaces()
	if err != nil {
		return nil, rasterDocument{}, err
	}
	defer closeFonts()

	parsed := parseRasterReceipt(receipt)
	canvas := newReceiptCanvas(fonts)
	if err := canvas.drawReceipt(parsed); err != nil {
		return nil, rasterDocument{}, err
	}
	imageHeight := canvas.y + printerMarginYDots
	if imageHeight <= 0 || imageHeight > printerMaxHeightDots {
		return nil, rasterDocument{}, fmt.Errorf("receipt raster height %d exceeds safe limit %d", imageHeight, printerMaxHeightDots)
	}
	cropped := image.NewGray(image.Rect(0, 0, printerWidthDots, imageHeight))
	stddraw.Draw(cropped, cropped.Bounds(), image.White, image.Point{}, stddraw.Src)
	stddraw.Draw(cropped, cropped.Bounds(), canvas.image, image.Point{}, stddraw.Src)

	heightMM := max(50, int(math.Ceil(float64(imageHeight)*25.4/printerDPI)))
	return cropped, rasterDocument{
		WidthDots:  printerWidthDots,
		HeightDots: imageHeight,
		Media:      fmt.Sprintf("Custom.72x%dmm", heightMM),
	}, nil
}

func binaryDeviceK(bitmap *image.Gray) []byte {
	bounds := bitmap.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	pixels := make([]byte, width*height)
	for y := range height {
		sourceOffset := bitmap.PixOffset(bounds.Min.X, bounds.Min.Y+y)
		source := bitmap.Pix[sourceOffset : sourceOffset+width]
		target := pixels[y*width : (y+1)*width]
		for x, value := range source {
			if value < 128 {
				target[x] = 255
			}
		}
	}
	return pixels
}

func loadPrinterFontSource() printerFontSource {
	cachedPrinterFonts.Do(func() {
		configured := strings.TrimSpace(os.Getenv("AEXP_PRINTER_FONT"))
		candidates := printerFontCandidates
		if configured != "" {
			candidates = append([]string{configured}, candidates...)
		}
		for _, path := range candidates {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			regular, err := parseOpenTypeFont(data)
			if err != nil {
				continue
			}
			cachedPrinterFontSource.regular = regular
			cachedPrinterFontSource.bold = regular
			return
		}
		regular, regularErr := opentype.Parse(goregular.TTF)
		bold, boldErr := opentype.Parse(gobold.TTF)
		if regularErr != nil || boldErr != nil {
			cachedPrinterFontSource.err = fmt.Errorf("load fallback print fonts: regular=%v bold=%v", regularErr, boldErr)
			return
		}
		cachedPrinterFontSource.regular = regular
		cachedPrinterFontSource.bold = bold
	})
	return cachedPrinterFontSource
}

func parseOpenTypeFont(data []byte) (*opentype.Font, error) {
	if collection, err := opentype.ParseCollection(data); err == nil {
		if collection.NumFonts() == 0 {
			return nil, fmt.Errorf("font collection is empty")
		}
		return collection.Font(0)
	}
	return opentype.Parse(data)
}

func newPrinterFaces() (printerFaces, func(), error) {
	source := loadPrinterFontSource()
	if source.err != nil {
		return printerFaces{}, func() {}, source.err
	}
	type faceSpec struct {
		target *xfont.Face
		font   *opentype.Font
		size   float64
	}
	faces := printerFaces{}
	specs := []faceSpec{
		{&faces.title, source.bold, 27},
		{&faces.status, source.bold, 19},
		{&faces.section, source.bold, 21},
		{&faces.body, source.regular, 22},
		{&faces.compact, source.regular, 20},
		{&faces.summary, source.regular, 18},
		{&faces.metadata, source.regular, 16},
	}
	opened := make([]xfont.Face, 0, len(specs))
	closeFaces := func() {
		for _, face := range opened {
			if closer, ok := face.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
		}
	}
	for _, spec := range specs {
		face, err := opentype.NewFace(spec.font, &opentype.FaceOptions{Size: spec.size, DPI: 72, Hinting: xfont.HintingFull})
		if err != nil {
			closeFaces()
			return printerFaces{}, func() {}, fmt.Errorf("create print font face: %w", err)
		}
		*spec.target = face
		opened = append(opened, face)
	}
	return faces, closeFaces, nil
}

func parseRasterReceipt(receipt string) rasterReceipt {
	lines := strings.Split(strings.ReplaceAll(receipt, "\r\n", "\n"), "\n")
	out := rasterReceipt{Title: "AEXP RUN", Status: "RUN"}
	section := "run"
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || isReceiptRule(trimmed) {
			continue
		}
		if trimmed == "AEXP PRINTER TEST" {
			out.Title = "AEXP TEST"
			out.Status = "TEST"
			out.Kind = "PRINTER"
			out.GPU = "CUPS"
			continue
		}
		if trimmed == "TEST RECEIPT - NOT AN EXPERIMENT" {
			out.Warning = "NOT AN EXPERIMENT"
			continue
		}
		if trimmed == "CUPS + FEED + CUT CHECK" {
			out.RunFields = append(out.RunFields, receiptField{Key: "check", Value: "CUPS / FEED / CUT"})
			continue
		}
		if strings.HasPrefix(trimmed, "AEXP RUN ") {
			out.Status = strings.TrimSpace(strings.TrimPrefix(trimmed, "AEXP RUN "))
			continue
		}
		switch trimmed {
		case "HYPERPARAMETERS":
			section = "params"
			continue
		case "HYPERPARAM     VALUE":
			continue
		case "COMMAND":
			section = "command"
			continue
		case "PLUMBING ONLY - NOT A RESULT":
			out.Warning = trimmed
			continue
		}
		if section == "command" {
			out.Command = append(out.Command, trimmed)
			continue
		}
		if section == "params" {
			key, value := splitReceiptParamRow(raw)
			if key == "" && len(out.Params) > 0 {
				out.Params[len(out.Params)-1].Value = strings.TrimSpace(out.Params[len(out.Params)-1].Value + " " + value)
			} else if key != "" {
				out.Params = append(out.Params, receiptField{Key: key, Value: value})
			}
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		field := receiptField{Key: key, Value: value}
		switch key {
		case "duration", "exit", "data", "finished":
			out.ResultFields = append(out.ResultFields, field)
		default:
			out.RunFields = append(out.RunFields, field)
		}
		switch key {
		case "run":
			out.RunID = value
		case "kind":
			out.Kind = strings.ToUpper(value)
		case "gpu":
			out.GPU = "GPU " + value
		case "started", "finished":
			out.Timestamp = value
		}
	}
	if out.Kind == "" {
		out.Kind = "RUN"
	}
	if out.GPU == "" {
		out.GPU = "GPU -"
	}
	return out
}

func isReceiptRule(value string) bool {
	if value == "" {
		return false
	}
	hasRule := false
	for _, r := range value {
		if unicode.IsSpace(r) {
			continue
		}
		if r != '=' && r != '-' && r != '━' {
			return false
		}
		hasRule = true
	}
	return hasRule
}

func splitReceiptParamRow(line string) (string, string) {
	runes := []rune(line)
	keyEnd := min(receiptParamKeyWidth, len(runes))
	key := strings.TrimSpace(string(runes[:keyEnd]))
	value := ""
	if keyEnd < len(runes) {
		value = strings.TrimSpace(string(runes[keyEnd:]))
	}
	return key, value
}

type receiptCanvas struct {
	image *image.Gray
	fonts printerFaces
	y     int
}

func newReceiptCanvas(fonts printerFaces) *receiptCanvas {
	img := image.NewGray(image.Rect(0, 0, printerWidthDots, printerMaxHeightDots))
	stddraw.Draw(img, img.Bounds(), image.White, image.Point{}, stddraw.Src)
	return &receiptCanvas{image: img, fonts: fonts, y: printerMarginYDots}
}

func (c *receiptCanvas) drawReceipt(receipt rasterReceipt) error {
	left := printerMarginXDots
	right := printerWidthDots - printerMarginXDots

	c.fillRect(left, c.y, printerContentWidth, printerHeaderHeight, 0)
	c.drawText(c.fonts.title, receipt.Title, left+8, c.y, printerHeaderHeight, 255)
	status := receiptStatusLabel(receipt.Status)
	status = truncatePixelText(c.fonts.status, status, printerContentWidth/2)
	statusWidth := measurePixelText(c.fonts.status, status)
	c.drawText(c.fonts.status, status, right-8-statusWidth, c.y, printerHeaderHeight, 255)
	c.y += printerHeaderHeight + 5

	meta := receipt.Kind
	if receipt.Warning != "" {
		meta = "! " + receipt.Kind
	}
	c.drawText(c.fonts.metadata, meta, left, c.y, printerSmallLineHeight, 0)
	if receipt.Timestamp != "" {
		timestamp := truncatePixelText(c.fonts.metadata, receipt.Timestamp, printerContentWidth*2/3)
		width := measurePixelText(c.fonts.metadata, timestamp)
		c.drawText(c.fonts.metadata, timestamp, right-width, c.y, printerSmallLineHeight, 0)
	}
	c.y += printerSmallLineHeight + 5

	c.strokeRect(left, c.y, printerContentWidth, printerSummaryHeight, 2)
	summaryValues := []string{receipt.Kind, receipt.GPU, strings.ToUpper(receipt.Status)}
	cellWidth := printerContentWidth / 3
	for index, value := range summaryValues {
		cellX := left + index*cellWidth
		if index > 0 {
			c.fillRect(cellX, c.y, 1, printerSummaryHeight, 0)
		}
		value = truncatePixelText(c.fonts.summary, value, cellWidth-8)
		width := measurePixelText(c.fonts.summary, value)
		c.drawText(c.fonts.summary, value, cellX+(cellWidth-width)/2, c.y, printerSummaryHeight, 0)
	}
	c.y += printerSummaryHeight + 11

	if receipt.Warning != "" {
		c.strokeRect(left, c.y, printerContentWidth, 30, 2)
		warning := truncatePixelText(c.fonts.status, receipt.Warning, printerContentWidth-16)
		c.drawText(c.fonts.status, warning, left+8, c.y, 30, 0)
		c.y += 38
	}

	firstSection := true
	drawSection := func(label string) {
		if !firstSection {
			c.y += 7
		}
		firstSection = false
		c.drawSectionHeader(label)
	}

	if len(receipt.RunFields) > 0 {
		label := "RUN"
		if receipt.Kind == "PRINTER" {
			label = "CHECK"
		}
		drawSection(label)
		for _, field := range receipt.RunFields {
			c.drawField(field)
		}
	}
	if len(receipt.Params) > 0 {
		drawSection("PARAMS")
		for _, field := range receipt.Params {
			c.drawField(field)
		}
	}
	if len(receipt.Command) > 0 {
		drawSection("COMMAND")
		command := strings.Join(receipt.Command, " ")
		for _, line := range wrapPixelText(c.fonts.compact, command, printerContentWidth) {
			c.drawText(c.fonts.compact, line, left, c.y, printerBodyLineHeight, 0)
			c.y += printerBodyLineHeight
		}
	}
	if len(receipt.ResultFields) > 0 {
		drawSection("RESULT")
		for _, field := range receipt.ResultFields {
			c.drawField(field)
		}
	}

	c.y += 8
	c.fillRect(left, c.y, printerContentWidth, 2, 0)
	c.y += 7
	footer := "# " + receipt.RunID
	if receipt.RunID == "" {
		footer = "# aexp"
	}
	footer += " · " + strings.ToLower(receipt.Status)
	footer = truncatePixelText(c.fonts.metadata, footer, printerContentWidth)
	footerWidth := measurePixelText(c.fonts.metadata, footer)
	c.drawText(c.fonts.metadata, footer, right-footerWidth, c.y, printerSmallLineHeight, 0)
	c.y += printerSmallLineHeight
	return nil
}

func (c *receiptCanvas) drawSectionHeader(label string) {
	left := printerMarginXDots
	right := printerWidthDots - printerMarginXDots
	labelWidth := measurePixelText(c.fonts.section, label)
	tagWidth := max(82, labelWidth+24)
	c.fillRect(left, c.y, tagWidth, printerSectionHeight, 0)
	c.drawText(c.fonts.section, label, left+(tagWidth-labelWidth)/2, c.y, printerSectionHeight, 255)
	lineX := left + tagWidth + 8
	c.fillRect(lineX, c.y+printerSectionHeight/2, right-lineX, 2, 0)
	c.y += printerSectionHeight + 5
}

func (c *receiptCanvas) drawField(field receiptField) {
	left := printerMarginXDots
	keyWidth := 126
	valueX := left + keyWidth
	valueWidth := printerContentWidth - keyWidth
	key := truncatePixelText(c.fonts.metadata, strings.ToUpper(field.Key), keyWidth-10)
	lines := wrapPixelText(c.fonts.body, field.Value, valueWidth)
	if len(lines) == 0 {
		lines = []string{"-"}
	}
	c.drawText(c.fonts.metadata, key, left, c.y, printerBodyLineHeight, 0)
	for index, line := range lines {
		c.drawText(c.fonts.body, line, valueX, c.y+index*printerBodyLineHeight, printerBodyLineHeight, 0)
	}
	c.y += len(lines) * printerBodyLineHeight
}

func (c *receiptCanvas) drawText(face xfont.Face, text string, x, top, lineHeight int, gray uint8) {
	metrics := face.Metrics()
	textHeight := metrics.Height.Ceil()
	baseline := top + (lineHeight-textHeight)/2 + metrics.Ascent.Ceil()
	drawer := xfont.Drawer{
		Dst:  c.image,
		Src:  image.NewUniform(color.Gray{Y: gray}),
		Face: face,
		Dot:  fixed.P(x, baseline),
	}
	drawer.DrawString(text)
}

func (c *receiptCanvas) fillRect(x, y, width, height int, gray uint8) {
	if width <= 0 || height <= 0 {
		return
	}
	rect := image.Rect(x, y, x+width, y+height)
	stddraw.Draw(c.image, rect, image.NewUniform(color.Gray{Y: gray}), image.Point{}, stddraw.Src)
}

func (c *receiptCanvas) strokeRect(x, y, width, height, stroke int) {
	c.fillRect(x, y, width, stroke, 0)
	c.fillRect(x, y+height-stroke, width, stroke, 0)
	c.fillRect(x, y, stroke, height, 0)
	c.fillRect(x+width-stroke, y, stroke, height, 0)
}

func measurePixelText(face xfont.Face, text string) int {
	return xfont.MeasureString(face, text).Ceil()
}

func truncatePixelText(face xfont.Face, text string, width int) string {
	if measurePixelText(face, text) <= width {
		return text
	}
	runes := []rune(text)
	for len(runes) > 1 && measurePixelText(face, string(runes)+"…") > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

func wrapPixelText(face xfont.Face, text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" || width <= 0 {
		return nil
	}
	if measurePixelText(face, text) <= width {
		return []string{text}
	}
	runes := []rune(text)
	lines := make([]string, 0)
	for len(runes) > 0 {
		cut := 0
		lastSpace := -1
		for index, r := range runes {
			candidate := string(runes[:index+1])
			if measurePixelText(face, candidate) > width {
				break
			}
			cut = index + 1
			if unicode.IsSpace(r) {
				lastSpace = index
			}
		}
		if cut == 0 {
			cut = 1
		} else if cut < len(runes) && lastSpace >= cut/2 {
			cut = lastSpace
		}
		line := strings.TrimSpace(string(runes[:cut]))
		if line != "" {
			lines = append(lines, line)
		}
		runes = runes[cut:]
		for len(runes) > 0 && unicode.IsSpace(runes[0]) {
			runes = runes[1:]
		}
	}
	return lines
}

func receiptStatusLabel(status string) string {
	status = strings.ToUpper(strings.TrimSpace(status))
	switch status {
	case "STARTED", "RUNNING":
		return "▶ " + status
	case "SUCCEEDED", "COMPLETED":
		return "✓ " + status
	case "FAILED", "CANCELLED":
		return "✕ " + status
	default:
		return status
	}
}
