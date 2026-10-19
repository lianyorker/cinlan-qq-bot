package media

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	iqCanvasW     = 1320
	iqPad         = 28
	iqCellW       = 170
	iqCellH       = 96
	iqNameColW    = 170
	iqVendorColW  = 64
	iqHeaderH     = 46
	iqRowGap      = 10
	iqTitleH      = 120
	iqFooterH     = 40
	iqModelRowGap = 8
)

var (
	iqBG        = color.RGBA{250, 251, 253, 255}
	iqHeaderBG  = color.RGBA{45, 55, 95, 255}
	iqTitleFG   = color.RGBA{30, 34, 48, 255}
	iqSubFG     = color.RGBA{120, 127, 145, 255}
	iqModelFG   = color.RGBA{40, 45, 60, 255}
	iqSubModel  = color.RGBA{140, 146, 160, 255}
	iqVendorBG  = color.RGBA{45, 55, 95, 255}
	iqVendorFG  = color.RGBA{235, 240, 255, 255}
	iqHeadRowFG = color.RGBA{235, 240, 255, 255}
	iqStarFG    = color.RGBA{245, 165, 35, 255}
	iqEmptyBG   = color.RGBA{238, 240, 244, 255}
)

// score tiers: (min, bg, fg) — 柔和低饱和
var iqTiers = []struct {
	min float64
	bg  color.RGBA
	fg  color.RGBA
}{
	{105, color.RGBA{34, 139, 90, 255}, color.RGBA{255, 255, 255, 255}},
	{95, color.RGBA{200, 235, 215, 255}, color.RGBA{28, 95, 60, 255}},
	{85, color.RGBA{224, 238, 180, 255}, color.RGBA{100, 110, 25, 255}},
	{70, color.RGBA{250, 215, 155, 255}, color.RGBA{135, 75, 15, 255}},
	{0, color.RGBA{248, 198, 194, 255}, color.RGBA{150, 45, 40, 255}},
}

type iqFonts struct {
	title, score, mid, small, tiny font.Face
}

func iqCellColor(score float64) (color.RGBA, color.RGBA) {
	for _, tier := range iqTiers {
		if score >= tier.min {
			return tier.bg, tier.fg
		}
	}
	return iqTiers[len(iqTiers)-1].bg, iqTiers[len(iqTiers)-1].fg
}

func renderIQChart(grid [][]iqCell, out string, fontPath string) error {
	face, err := loadFace(fontPath)
	if err != nil {
		return err
	}
	fonts := iqFonts{
		title: newFace(face, 34),
		score: newFace(face, 26),
		mid:   newFace(face, 18),
		small: newFace(face, 14),
		tiny:  newFace(face, 12),
	}
	defer func() {
		for _, f := range []font.Face{fonts.title, fonts.score, fonts.mid, fonts.small, fonts.tiny} {
			_ = f.Close()
		}
	}()

	rows := len(grid)
	totalH := iqPad + iqTitleH + iqHeaderH + rows*(iqCellH+iqModelRowGap) + iqFooterH + iqPad
	img := image.NewRGBA(image.Rect(0, 0, iqCanvasW, totalH))
	draw.Draw(img, img.Bounds(), &image.Uniform{iqBG}, image.Point{}, draw.Src)

	// 标题块
	drawRect(img, image.Rect(0, 0, iqCanvasW, iqPad+iqTitleH), iqBG)
	drawText(img, fonts.title, iqTitleFG, iqPad, iqPad+42, "模型 IQ 天梯")
	drawText(img, fonts.small, iqSubFG, iqPad, iqPad+72,
		"Codex 雷达「雷达Bench」真实编程任务实测 · 按厂商分类，组内按最高分从高到低排列")
	drawText(img, fonts.small, iqSubFG, iqPad, iqPad+96,
		fmt.Sprintf("得分 = 已测题目最近 3 次得分平均 × 1.5，满分 150；格内：得分 · 题数 · 单题花费 · 耗时   最新评分 %s",
			time.Now().Format("2006-01-02 15:04")))

	y := iqPad + iqTitleH
	// 表头行
	drawRect(img, image.Rect(iqPad, y, iqCanvasW-iqPad, y+iqHeaderH), iqHeaderBG)
	headX := iqPad + iqVendorColW + iqNameColW
	drawText(img, fonts.mid, iqHeadRowFG, iqPad+14, y+30, "模型")
	for j, effort := range iqEfforts {
		drawText(img, fonts.mid, iqHeadRowFG,
			headX+j*iqCellW+iqCellW/2-textW(fonts.mid, effort)/2, y+30, effort)
	}
	y += iqHeaderH + iqRowGap

	// 每行：厂商块 + 模型名 + 分数格
	best := -1.0
	for _, row := range grid {
		for _, cell := range row {
			if cell.ok && cell.score > best {
				best = cell.score
			}
		}
	}
	for i, row := range grid {
		rowTop := y
		model := iqModels[i]
		if i == 0 { // 厂商标签（OpenAI·GPT 合并块）
			vh := rows*(iqCellH+iqModelRowGap) - iqModelRowGap
			drawRect(img, image.Rect(iqPad, rowTop, iqPad+iqVendorColW, rowTop+vh), iqVendorBG)
			drawVerticalText(img, fonts.mid, iqVendorFG, iqPad+iqVendorColW/2,
				rowTop+vh/2, "OpenAI · GPT")
		}
		nameX := iqPad + iqVendorColW
		drawText(img, fonts.mid, iqModelFG, nameX+8, rowTop+34, model)
		drawText(img, fonts.tiny, iqSubModel, nameX+8, rowTop+58, "Codex")
		for j, cell := range row {
			cx := headX + j*iqCellW
			if !cell.ok {
				drawRoundRect(img, image.Rect(cx, rowTop, cx+iqCellW-8, rowTop+iqCellH-2),
					iqEmptyBG, 10, iqBG)
				drawText(img, fonts.small, iqSubModel, cx+iqCellW/2-6, rowTop+iqCellH/2+5, "—")
				continue
			}
			bg, fg := iqCellColor(cell.score)
			drawRoundRect(img, image.Rect(cx, rowTop, cx+iqCellW-8, rowTop+iqCellH-2), bg, 10, iqBG)
			scoreText := fmt.Sprintf("%.1f", cell.score)
			sx := cx + (iqCellW-6)/2 - textW(fonts.score, scoreText)/2
			drawText(img, fonts.score, fg, sx, rowTop+38, scoreText)
			if cell.score == best {
				drawText(img, fonts.small, iqStarFG, cx+iqCellW-28, rowTop+24, "★")
			}
			line2 := fmt.Sprintf("%d/64", cell.coverage)
			drawText(img, fonts.tiny, fg,
				cx+(iqCellW-6)/2-textW(fonts.tiny, line2)/2, rowTop+60, line2)
			line3 := fmt.Sprintf("$%.2f · %.0f分钟", cell.cost, cell.minutes)
			drawText(img, fonts.tiny, fg,
				cx+(iqCellW-6)/2-textW(fonts.tiny, line3)/2, rowTop+80, line3)
		}
		y += iqCellH + iqModelRowGap
	}

	// 页脚
	drawText(img, fonts.tiny, iqSubFG, iqPad, y+iqFooterH-10,
		"数据来源：Codex 雷达 codexradar.com · ★ 为该模型最高分")

	file, err := os.Create(out)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	return encoder.Encode(file, img)
}

func loadFace(path string) (*opentype.Font, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return opentype.Parse(data)
}

func newFace(f *opentype.Font, size float64) font.Face {
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    size,
		DPI:     96,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return basicfont.Face7x13
	}
	return face
}

func textW(face font.Face, s string) int {
	width := 0
	prev := rune(-1)
	for _, r := range s {
		if prev != -1 {
			width += face.Kern(prev, r).Ceil()
		}
		advance, ok := face.GlyphAdvance(r)
		if ok {
			width += advance.Ceil()
		}
		prev = r
	}
	return width
}

func drawText(img draw.Image, face font.Face, fg color.RGBA, x, y int, s string) {
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(fg),
		Face: face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(s)
}

func drawVerticalText(img draw.Image, face font.Face, fg color.RGBA, cx, cy int, s string) {
	runes := []rune(s)
	lineH := face.Metrics().Height.Ceil()
	totalH := len(runes) * lineH
	y := cy - totalH/2 + lineH - face.Metrics().Descent.Ceil()
	for _, r := range runes {
		if r == ' ' {
			y += lineH / 2
			continue
		}
		adv, _ := face.GlyphAdvance(r)
		x := cx - adv.Ceil()/2
		drawText(img, face, fg, x, y, string(r))
		y += lineH
	}
}

func drawRect(img draw.Image, rect image.Rectangle, c color.RGBA) {
	draw.Draw(img, rect, &image.Uniform{c}, image.Point{}, draw.Src)
}

// drawRoundRect fills rect with c and cuts the four corners back to cornerBG,
// producing a simple rounded-rectangle look without vector deps.
func drawRoundRect(
	img draw.Image,
	rect image.Rectangle,
	c color.RGBA,
	radius int,
	cornerBG color.RGBA,
) {
	drawRect(img, rect, c)
	corner := &image.Uniform{cornerBG}
	for _, pt := range [4]image.Point{
		rect.Min,
		{rect.Max.X, rect.Min.Y},
		{rect.Min.X, rect.Max.Y},
		rect.Max,
	} {
		cx, cy := pt.X, pt.Y
		for dy := 0; dy < radius; dy++ {
			for dx := 0; dx < radius; dx++ {
				if dx*dx+dy*dy > radius*radius {
					px, py := cx, cy
					switch {
					case pt.X == rect.Max.X && pt.Y == rect.Max.Y:
						px, py = cx-dx, cy-dy
					case pt.X == rect.Max.X:
						px, py = cx-dx, cy+dy
					case pt.Y == rect.Max.Y:
						px, py = cx+dx, cy-dy
					default:
						px, py = cx+dx, cy+dy
					}
					if px >= rect.Min.X && px < rect.Max.X && py >= rect.Min.Y && py < rect.Max.Y {
						draw.Draw(img, image.Rect(px, py, px+1, py+1), corner, image.Point{}, draw.Src)
					}
				}
			}
		}
	}
}
