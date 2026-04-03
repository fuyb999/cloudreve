package manager

import (
	"strings"
	"unicode"

	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

func evaluateFTSExtractionQuality(doc *searcher.SearchFileDocument, cfg *setting.FTSExternalExtractorSetting) *externalFTSQualityReport {
	report := &externalFTSQualityReport{Accepted: true}
	if cfg == nil || !cfg.Quality.Enabled {
		return report
	}

	text := buildFTSQualityText(doc)
	runes := []rune(text)
	report.TextLength = len(runes)
	if report.TextLength == 0 {
		report.Accepted = false
		report.Reasons = append(report.Reasons, "text_empty")
		return report
	}

	if report.TextLength < cfg.Quality.MinTextLength {
		report.Accepted = false
		report.Reasons = append(report.Reasons, "text_too_short")
	}

	for _, r := range runes {
		if !unicode.IsSpace(r) {
			report.VisibleCount++
		}

		switch {
		case r == '\uFFFD':
			report.ReplacementCount++
		case unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t':
			report.ControlCount++
		}

		if unicode.IsPrint(r) || unicode.IsSpace(r) {
			report.PrintableCount++
		}
	}

	total := float64(report.TextLength)
	visibleTotal := float64(report.VisibleCount)
	if visibleTotal <= 0 {
		visibleTotal = total
	}
	report.ReplacementRatio = float64(report.ReplacementCount) / total
	report.ControlRatio = float64(report.ControlCount) / total
	report.PrintableRatio = float64(report.PrintableCount) / total

	report.BoxGlyphCount, report.MaxBoxGlyphRun = countFTSBoxGlyphs(runes)
	if visibleTotal > 0 {
		report.BoxGlyphRatio = float64(report.BoxGlyphCount) / visibleTotal
	}

	if report.ReplacementRatio > cfg.Quality.MaxReplacementRatio {
		report.Accepted = false
		report.Reasons = append(report.Reasons, "replacement_ratio_high")
	}
	if report.ControlRatio > cfg.Quality.MaxControlCharRatio {
		report.Accepted = false
		report.Reasons = append(report.Reasons, "control_ratio_high")
	}
	if report.PrintableRatio < cfg.Quality.MinPrintableRatio {
		report.Accepted = false
		report.Reasons = append(report.Reasons, "printable_ratio_low")
	}
	fontBoxMinCount := cfg.Quality.FontBoxMinCount
	if fontBoxMinCount <= 0 {
		fontBoxMinCount = 4
	}
	fontBoxMinRun := cfg.Quality.FontBoxMinRun
	if fontBoxMinRun <= 0 {
		fontBoxMinRun = 3
	}
	fontBoxMinRatio := cfg.Quality.FontBoxMinRatio
	if fontBoxMinRatio <= 0 {
		fontBoxMinRatio = 0.35
	}

	if report.BoxGlyphCount >= fontBoxMinCount &&
		(report.MaxBoxGlyphRun >= fontBoxMinRun || report.BoxGlyphRatio >= fontBoxMinRatio) {
		report.Accepted = false
		report.Reasons = append(report.Reasons, "font_issue_box_glyphs")
	}

	return report
}

func countFTSBoxGlyphs(runes []rune) (count int, maxRun int) {
	run := 0
	for _, r := range runes {
		if isFTSFontBoxGlyph(r) {
			count++
			run++
			if run > maxRun {
				maxRun = run
			}
			continue
		}

		run = 0
	}

	return count, maxRun
}

func isFTSFontBoxGlyph(r rune) bool {
	switch r {
	case '□', '■', '▢', '▣', '▤', '▥', '▦', '▧', '▨', '▩', '▬', '▭', '▮', '▯', '◻', '◼', '◽', '◾', '☐', '☑', '☒', '⬚', '⬛', '⬜':
		return true
	default:
		return false
	}
}

func buildFTSQualityText(doc *searcher.SearchFileDocument) string {
	if doc == nil {
		return ""
	}

	parts := make([]string, 0, 1+len(doc.Attachments))
	if text := strings.TrimSpace(doc.Content); text != "" {
		parts = append(parts, text)
	}
	for _, attachment := range doc.Attachments {
		if text := strings.TrimSpace(attachment.Content); text != "" {
			parts = append(parts, text)
		}
	}

	return strings.Join(parts, "\n")
}
