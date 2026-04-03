// Atualiza front matter dos .md para alinhar com SEO, AIO/AEO e GEO do site
// (seo_title, description, youtube_id). Por omissão só mostra o que mudaria (dry-run).
//
// Uso:
//
//	go run scripts/seo-frontmatter.go -root content              # dry-run (só artigos datados)
//	go run scripts/seo-frontmatter.go -root content -all         # todos os .md
//	go run scripts/seo-frontmatter.go -root content -write
//
// Nota: -write reescreve o YAML (comentários no front matter podem ser perdidos).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	maxSeoTitleRunes       = 58
	maxDescriptionRunes    = 155
	maxSummaryForDescRunes = 155
)

var (
	reYouTubeShortcode = regexp.MustCompile(`(?i)\{\{\s*<\s*youtube[^>]*\bid\s*=\s*"([^"]+)"`)
	reYouTubeLegacy    = regexp.MustCompile(`(?i)youtube\.com/embed/([a-zA-Z0-9_-]{6,})`)
)

func main() {
	root := flag.String("root", "content", "pasta raiz com ficheiros .md")
	write := flag.Bool("write", false, "gravar alterações (sem esta flag: apenas relatório)")
	onlyDated := flag.Bool("only-dated", true, "apenas artigos em content/AAAA/MM/DD/*/index.md (recomendado)")
	allPages := flag.Bool("all", false, "processar todos os .md (desativa only-dated)")
	flag.Parse()
	scopeDated := *onlyDated && !*allPages

	var changedFiles, scanned int
	err := filepath.WalkDir(*root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		rel, _ := filepath.Rel(*root, path)
		rel = filepath.ToSlash(rel)
		if scopeDated && !isDatedArticlePath(rel) {
			return nil
		}

		scanned++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fm, body, ok := splitFrontMatter(data)
		if !ok {
			return nil
		}

		var m map[string]any
		if err := yaml.Unmarshal([]byte(fm), &m); err != nil {
			fmt.Fprintf(os.Stderr, "YAML %s: %v\n", rel, err)
			return nil
		}
		if m == nil {
			m = map[string]any{}
		}

		notes := adaptFrontMatter(m, body)
		if len(notes) == 0 {
			return nil
		}

		out, err := yaml.Marshal(m)
		if err != nil {
			fmt.Fprintf(os.Stderr, "marshal %s: %v\n", rel, err)
			return nil
		}
		newFM := strings.TrimSuffix(string(out), "\n") + "\n"
		newData := append([]byte("---\n"), []byte(newFM)...)
		newData = append(newData, []byte("---\n")...)
		newData = append(newData, body...)

		if bytes.Equal(data, newData) {
			return nil
		}

		changedFiles++
		fmt.Printf("\n## %s\n", rel)
		for _, n := range notes {
			fmt.Printf("  • %s\n", n)
		}
		if *write {
			if err := os.WriteFile(path, newData, 0o644); err != nil {
				return fmt.Errorf("escrever %s: %w", path, err)
			}
			fmt.Printf("  → gravado\n")
		} else {
			fmt.Printf("  (use -write para aplicar)\n")
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("\nResumo: %d .md analisados, %d com alterações propostas", scanned, changedFiles)
	if !*write && changedFiles > 0 {
		fmt.Print(" (dry-run)")
	}
	fmt.Println(".")
}

func isDatedArticlePath(rel string) bool {
	ok, _ := filepath.Match("[0-9][0-9][0-9][0-9]/[0-9][0-9]/[0-9][0-9]/*/index.md", rel)
	return ok
}

func splitFrontMatter(data []byte) (fm string, body []byte, ok bool) {
	if !bytes.HasPrefix(data, []byte("---")) {
		return "", nil, false
	}
	// após primeira linha "---"
	i := 3
	if i < len(data) && data[i] == '\r' {
		i++
	}
	if i < len(data) && data[i] == '\n' {
		i++
	} else {
		return "", nil, false
	}
	closers := [][]byte{[]byte("\n---\n"), []byte("\n---\r\n")}
	var idx, clen int
	for _, c := range closers {
		p := bytes.Index(data[i:], c)
		if p >= 0 {
			idx = p
			clen = len(c)
			break
		}
	}
	if idx < 0 {
		return "", nil, false
	}
	fm = string(data[i : i+idx])
	body = data[i+idx+clen:]
	return fm, body, true
}

func adaptFrontMatter(m map[string]any, body []byte) []string {
	var notes []string
	bodyStr := string(body)

	// youtube_id a partir do shortcode / URL
	if _, has := m["youtube_id"]; !has {
		if id := extractYouTubeID(bodyStr); id != "" {
			m["youtube_id"] = id
			notes = append(notes, fmt.Sprintf("youtube_id: %q (detetado no corpo)", id))
		}
	}

	title := str(m["title"])
	if title == "" {
		return notes
	}

	// seo_title — só se o title for longo para redes / <title>
	if _, has := m["seo_title"]; !has && runesLen(title) > maxSeoTitleRunes {
		st := shortenForSeoTitle(title)
		m["seo_title"] = st
		notes = append(notes, fmt.Sprintf("seo_title: %q (derivado do title)", st))
	}

	// description (meta SEO / AIO) — curta; prioriza summary, senão início do corpo
	if d, has := m["description"]; !has || strings.TrimSpace(str(d)) == "" {
		desc := ""
		if s := str(m["summary"]); s != "" {
			desc = truncateRunes(strings.TrimSpace(s), maxSummaryForDescRunes)
			notes = append(notes, "description: derivado de summary (≤155 runes)")
		} else {
			desc = plainTextExcerpt(bodyStr, maxDescriptionRunes)
			if runesLen(desc) >= 40 {
				notes = append(notes, "description: derivado do texto do artigo")
			} else {
				desc = ""
			}
		}
		if desc != "" {
			m["description"] = desc
		}
	} else {
		cur := strings.TrimSpace(str(m["description"]))
		if runesLen(cur) > maxDescriptionRunes {
			m["description"] = truncateRunes(cur, maxDescriptionRunes)
			notes = append(notes, fmt.Sprintf("description: truncado para ≤%d runes", maxDescriptionRunes))
		}
	}

	return notes
}

func extractYouTubeID(s string) string {
	if m := reYouTubeShortcode.FindStringSubmatch(s); len(m) > 1 {
		return m[1]
	}
	if m := reYouTubeLegacy.FindStringSubmatch(s); len(m) > 1 {
		return m[1]
	}
	return ""
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func runesLen(s string) int {
	return utf8.RuneCountInString(s)
}

func truncateRunes(s string, max int) string {
	if runesLen(s) <= max {
		return s
	}
	r := []rune(s)
	if max <= 1 {
		return string(r[0]) + "…"
	}
	return string(r[:max-1]) + "…"
}

// Encurta título para Open Graph / <title> (≈58 runes, preferindo corte em espaço).
func shortenForSeoTitle(title string) string {
	title = strings.TrimSpace(title)
	if runesLen(title) <= maxSeoTitleRunes {
		return title
	}
	r := []rune(title)
	cut := maxSeoTitleRunes - 1
	if cut < 8 {
		return truncateRunes(title, maxSeoTitleRunes)
	}
	segment := string(r[:cut])
	if i := strings.LastIndex(segment, " "); i > 12 {
		return strings.TrimSpace(segment[:i]) + "…"
	}
	return truncateRunes(title, maxSeoTitleRunes)
}

func plainTextExcerpt(md string, maxRunes int) string {
	// remove blocos de código e shortcodes grosseiramente
	s := md
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?s)\{\{<[^>]+>\}\}`),
		regexp.MustCompile(`(?m)^#+\s+`),
		regexp.MustCompile(`\*\*([^*]+)\*\*`),
		regexp.MustCompile(`\*([^*]+)\*`),
		regexp.MustCompile("`[^`]+`"),
		regexp.MustCompile(`\[[^\]]*\]\([^)]*\)`),
	} {
		s = re.ReplaceAllString(s, " ")
	}
	s = strings.Join(strings.Fields(s), " ")
	return truncateRunes(strings.TrimSpace(s), maxRunes)
}
