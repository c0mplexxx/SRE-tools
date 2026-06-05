package bot

import (
	"bytes"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	graphWidth  = 1000
	graphHeight = 620
)

var graphPalette = []color.RGBA{
	{R: 97, G: 175, B: 254, A: 255},
	{R: 152, G: 195, B: 121, A: 255},
	{R: 229, G: 192, B: 123, A: 255},
	{R: 198, G: 120, B: 221, A: 255},
	{R: 224, G: 108, B: 117, A: 255},
	{R: 86, G: 182, B: 194, A: 255},
}

func RenderGraphCaption(graph InstanceGraph) string {
	tenant := strings.TrimSpace(graph.Tenant)
	if tenant == "" {
		tenant = TenantOne
	}
	command := strings.TrimSpace(graph.Command)
	if command == "" {
		command = "graph"
	}
	title := strings.TrimSpace(graph.Title)
	if title == "" {
		title = command
	}
	return fmt.Sprintf(
		"<b>%s</b> tenant <code>%s</code> | <code>%s</code> | <code>%s</code>",
		html.EscapeString(title),
		html.EscapeString(tenant),
		html.EscapeString(graph.Instance),
		html.EscapeString(graph.Range.Raw),
	)
}

func RenderGraphPNG(graph InstanceGraph) ([]byte, error) {
	if graphDataEmpty(graph.Series) {
		return nil, fmt.Errorf("graph has no valid points")
	}

	img := image.NewRGBA(image.Rect(0, 0, graphWidth, graphHeight))
	fillRect(img, img.Bounds(), color.RGBA{R: 8, G: 10, B: 14, A: 255})

	left, right, top, bottom := 76, graphWidth-28, 58, graphHeight-92
	plot := image.Rect(left, top, right, bottom)
	fillRect(img, plot, color.RGBA{R: 13, G: 16, B: 23, A: 255})

	yMin, yMax := graphYRange(graph)
	drawGrid(img, plot, yMin, yMax, graph.Unit)
	drawText(img, 24, 18, strings.ToUpper(strings.TrimSpace(graph.Title)), color.RGBA{R: 232, G: 235, B: 241, A: 255}, 2)
	subtitle := strings.ToUpper(strings.TrimSpace(graph.Instance) + " | " + strings.TrimSpace(graph.Range.Raw) + " | STEP " + prometheusDuration(graph.Range.Step))
	drawText(img, 24, 42, subtitle, color.RGBA{R: 145, G: 155, B: 171, A: 255}, 1)

	for i, series := range sortedGraphSeries(graph.Series) {
		drawSeries(img, plot, series, graph.Range, yMin, yMax, graphPalette[i%len(graphPalette)])
	}
	drawLegend(img, sortedGraphSeries(graph.Series), 24, graphHeight-64)
	drawBorder(img, plot, color.RGBA{R: 62, G: 68, B: 82, A: 255})

	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, fmt.Errorf("encode graph PNG: %w", err)
	}
	return out.Bytes(), nil
}

func graphYRange(graph InstanceGraph) (float64, float64) {
	minValue := math.Inf(1)
	maxValue := math.Inf(-1)
	for _, series := range graph.Series {
		for _, point := range series.Points {
			if !point.Valid {
				continue
			}
			if point.Value < minValue {
				minValue = point.Value
			}
			if point.Value > maxValue {
				maxValue = point.Value
			}
		}
	}
	if math.IsInf(minValue, 0) || math.IsInf(maxValue, 0) {
		return 0, 1
	}
	switch graph.Unit {
	case graphUnitPercent:
		minValue = 0
		if maxValue <= 100 {
			maxValue = 100
		}
	case graphUnitBits:
		minValue = 0
	default:
		if minValue > 0 {
			minValue = 0
		}
	}
	if maxValue <= minValue {
		maxValue = minValue + 1
	}
	padding := (maxValue - minValue) * 0.08
	if graph.Unit == graphUnitPercent {
		return minValue, maxValue
	}
	return minValue, maxValue + padding
}

func drawGrid(img *image.RGBA, plot image.Rectangle, yMin, yMax float64, unit string) {
	grid := color.RGBA{R: 41, G: 47, B: 61, A: 255}
	label := color.RGBA{R: 136, G: 146, B: 160, A: 255}
	for i := 0; i <= 4; i++ {
		y := plot.Min.Y + (plot.Dy()*i)/4
		drawLine(img, plot.Min.X, y, plot.Max.X, y, grid)
		value := yMax - (float64(i)/4)*(yMax-yMin)
		drawText(img, 12, y-4, strings.ToUpper(formatAxisValue(value, unit)), label, 1)
	}
	for i := 0; i <= 6; i++ {
		x := plot.Min.X + (plot.Dx()*i)/6
		drawLine(img, x, plot.Min.Y, x, plot.Max.Y, grid)
	}
}

func drawSeries(img *image.RGBA, plot image.Rectangle, series GraphSeries, window GraphRange, yMin, yMax float64, c color.RGBA) {
	if len(series.Points) == 0 {
		return
	}
	start, end := window.Start, window.End
	if start.IsZero() || end.IsZero() || !end.After(start) {
		start, end = pointTimeBounds(series.Points)
	}
	maxGap := window.Step * 5 / 2
	if maxGap <= 0 {
		maxGap = time.Minute
	}

	var prev image.Point
	var prevTime time.Time
	havePrev := false
	for _, point := range series.Points {
		if !point.Valid || point.Time.Before(start) || point.Time.After(end) {
			havePrev = false
			continue
		}
		x := plot.Min.X + int(point.Time.Sub(start).Seconds()/end.Sub(start).Seconds()*float64(plot.Dx()))
		y := plot.Max.Y - int((point.Value-yMin)/(yMax-yMin)*float64(plot.Dy()))
		current := image.Point{X: clampInt(x, plot.Min.X, plot.Max.X), Y: clampInt(y, plot.Min.Y, plot.Max.Y)}
		fillRect(img, image.Rect(current.X-2, current.Y-2, current.X+3, current.Y+3), c)
		if havePrev && point.Time.Sub(prevTime) <= maxGap {
			drawThickLine(img, prev.X, prev.Y, current.X, current.Y, c)
		}
		prev = current
		prevTime = point.Time
		havePrev = true
	}
}

func drawLegend(img *image.RGBA, series []GraphSeries, x, y int) {
	for i, item := range series {
		if i >= len(graphPalette) {
			break
		}
		itemX := x + (i%3)*300
		itemY := y + (i/3)*20
		fillRect(img, image.Rect(itemX, itemY, itemX+18, itemY+8), graphPalette[i%len(graphPalette)])
		drawText(img, itemX+26, itemY-2, strings.ToUpper(item.Name), color.RGBA{R: 205, G: 211, B: 222, A: 255}, 1)
	}
}

func sortedGraphSeries(series []GraphSeries) []GraphSeries {
	out := append([]GraphSeries(nil), series...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func pointTimeBounds(points []GraphPoint) (time.Time, time.Time) {
	var start, end time.Time
	for _, point := range points {
		if !point.Valid {
			continue
		}
		if start.IsZero() || point.Time.Before(start) {
			start = point.Time
		}
		if end.IsZero() || point.Time.After(end) {
			end = point.Time
		}
	}
	if start.IsZero() || end.IsZero() || !end.After(start) {
		now := time.Now()
		return now.Add(-time.Minute), now
	}
	return start, end
}

func formatAxisValue(value float64, unit string) string {
	switch unit {
	case graphUnitPercent:
		return fmt.Sprintf("%.0f%%", value)
	case graphUnitBits:
		return formatBits(value)
	case graphUnitLoad:
		if value >= 10 {
			return fmt.Sprintf("%.0f", value)
		}
		return fmt.Sprintf("%.1f", value)
	default:
		return fmt.Sprintf("%.1f", value)
	}
}

func formatBits(value float64) string {
	units := []string{"b/s", "Kb/s", "Mb/s", "Gb/s", "Tb/s"}
	idx := 0
	for math.Abs(value) >= 1000 && idx < len(units)-1 {
		value /= 1000
		idx++
	}
	if math.Abs(value) >= 10 || idx == 0 {
		return fmt.Sprintf("%.0f%s", value, units[idx])
	}
	return fmt.Sprintf("%.1f%s", value, units[idx])
}

func drawBorder(img *image.RGBA, rect image.Rectangle, c color.RGBA) {
	drawLine(img, rect.Min.X, rect.Min.Y, rect.Max.X, rect.Min.Y, c)
	drawLine(img, rect.Max.X, rect.Min.Y, rect.Max.X, rect.Max.Y, c)
	drawLine(img, rect.Max.X, rect.Max.Y, rect.Min.X, rect.Max.Y, c)
	drawLine(img, rect.Min.X, rect.Max.Y, rect.Min.X, rect.Min.Y, c)
}

func fillRect(img *image.RGBA, rect image.Rectangle, c color.RGBA) {
	draw.Draw(img, rect, image.NewUniform(c), image.Point{}, draw.Src)
}

func drawThickLine(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	drawLine(img, x0, y0, x1, y1, c)
	drawLine(img, x0, y0+1, x1, y1+1, c)
}

func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	dx := int(math.Abs(float64(x1 - x0)))
	sx := -1
	if x0 < x1 {
		sx = 1
	}
	dy := -int(math.Abs(float64(y1 - y0)))
	sy := -1
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		if image.Pt(x0, y0).In(img.Bounds()) {
			img.SetRGBA(x0, y0, c)
		}
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func drawText(img *image.RGBA, x, y int, text string, c color.RGBA, scale int) {
	if scale <= 0 {
		scale = 1
	}
	cursor := x
	for _, r := range strings.ToUpper(text) {
		glyph, ok := tinyFont[r]
		if !ok {
			glyph = tinyFont['?']
		}
		for row, pattern := range glyph {
			for col, pixel := range pattern {
				if pixel != '#' {
					continue
				}
				fillRect(img, image.Rect(cursor+col*scale, y+row*scale, cursor+(col+1)*scale, y+(row+1)*scale), c)
			}
		}
		cursor += (len(glyph[0]) + 1) * scale
		if cursor > graphWidth-8 {
			return
		}
	}
}

var tinyFont = map[rune][]string{
	' ': {"...", "...", "...", "...", "...", "...", "..."},
	'?': {".###.", "#...#", "...#.", "..#..", "..#..", ".....", "..#.."},
	'A': {".###.", "#...#", "#...#", "#####", "#...#", "#...#", "#...#"},
	'B': {"####.", "#...#", "#...#", "####.", "#...#", "#...#", "####."},
	'C': {".####", "#....", "#....", "#....", "#....", "#....", ".####"},
	'D': {"####.", "#...#", "#...#", "#...#", "#...#", "#...#", "####."},
	'E': {"#####", "#....", "#....", "####.", "#....", "#....", "#####"},
	'F': {"#####", "#....", "#....", "####.", "#....", "#....", "#...."},
	'G': {".####", "#....", "#....", "#.###", "#...#", "#...#", ".###."},
	'H': {"#...#", "#...#", "#...#", "#####", "#...#", "#...#", "#...#"},
	'I': {"#####", "..#..", "..#..", "..#..", "..#..", "..#..", "#####"},
	'J': {"..###", "...#.", "...#.", "...#.", "...#.", "#..#.", ".##.."},
	'K': {"#...#", "#..#.", "#.#..", "##...", "#.#..", "#..#.", "#...#"},
	'L': {"#....", "#....", "#....", "#....", "#....", "#....", "#####"},
	'M': {"#...#", "##.##", "#.#.#", "#...#", "#...#", "#...#", "#...#"},
	'N': {"#...#", "##..#", "#.#.#", "#..##", "#...#", "#...#", "#...#"},
	'O': {".###.", "#...#", "#...#", "#...#", "#...#", "#...#", ".###."},
	'P': {"####.", "#...#", "#...#", "####.", "#....", "#....", "#...."},
	'Q': {".###.", "#...#", "#...#", "#...#", "#.#.#", "#..#.", ".##.#"},
	'R': {"####.", "#...#", "#...#", "####.", "#.#..", "#..#.", "#...#"},
	'S': {".####", "#....", "#....", ".###.", "....#", "....#", "####."},
	'T': {"#####", "..#..", "..#..", "..#..", "..#..", "..#..", "..#.."},
	'U': {"#...#", "#...#", "#...#", "#...#", "#...#", "#...#", ".###."},
	'V': {"#...#", "#...#", "#...#", "#...#", "#...#", ".#.#.", "..#.."},
	'W': {"#...#", "#...#", "#...#", "#...#", "#.#.#", "##.##", "#...#"},
	'X': {"#...#", "#...#", ".#.#.", "..#..", ".#.#.", "#...#", "#...#"},
	'Y': {"#...#", "#...#", ".#.#.", "..#..", "..#..", "..#..", "..#.."},
	'Z': {"#####", "....#", "...#.", "..#..", ".#...", "#....", "#####"},
	'0': {".###.", "#...#", "#..##", "#.#.#", "##..#", "#...#", ".###."},
	'1': {"..#..", ".##..", "..#..", "..#..", "..#..", "..#..", ".###."},
	'2': {".###.", "#...#", "....#", "...#.", "..#..", ".#...", "#####"},
	'3': {"####.", "....#", "....#", ".###.", "....#", "....#", "####."},
	'4': {"#...#", "#...#", "#...#", "#####", "....#", "....#", "....#"},
	'5': {"#####", "#....", "#....", "####.", "....#", "....#", "####."},
	'6': {".###.", "#....", "#....", "####.", "#...#", "#...#", ".###."},
	'7': {"#####", "....#", "...#.", "..#..", ".#...", ".#...", ".#..."},
	'8': {".###.", "#...#", "#...#", ".###.", "#...#", "#...#", ".###."},
	'9': {".###.", "#...#", "#...#", ".####", "....#", "....#", ".###."},
	'/': {"....#", "...#.", "...#.", "..#..", ".#...", ".#...", "#...."},
	'.': {".....", ".....", ".....", ".....", ".....", ".##..", ".##.."},
	',': {".....", ".....", ".....", ".....", ".##..", ".##..", "##..."},
	':': {".....", ".##..", ".##..", ".....", ".##..", ".##..", "....."},
	'-': {".....", ".....", ".....", "####.", ".....", ".....", "....."},
	'_': {".....", ".....", ".....", ".....", ".....", ".....", "#####"},
	'|': {"..#..", "..#..", "..#..", "..#..", "..#..", "..#..", "..#.."},
	'%': {"##..#", "##.#.", "...#.", "..#..", ".#...", "#.##.", "#..##"},
	'(': {"...#.", "..#..", ".#...", ".#...", ".#...", "..#..", "...#."},
	')': {".#...", "..#..", "...#.", "...#.", "...#.", "..#..", ".#..."},
}
