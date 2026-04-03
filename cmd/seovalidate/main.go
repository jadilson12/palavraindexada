// seovalidate — apenas VERIFICA (read-only) o HTML já gerado.
//
// Responsabilidade: checagem de SEO, AEO (Speakable, texto citável), GEO (schema.org, autoria,
// breadcrumbs) e sinais úteis a AIO (metadados coerentes). Não altera ficheiros.
//
// Fluxo recomendado:  go run scripts/seo-frontmatter.go -root content -write  →  hugo  →  go run ./cmd/seovalidate -dir public
//
// Uso: go run ./cmd/seovalidate -dir public
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"
)

type severity int

const (
	sevOK severity = iota
	sevWarn
	sevErr
)

type finding struct {
	sev severity
	msg string
}

func main() {
	dir := flag.String("dir", "public", "pasta com HTML gerado pelo Hugo")
	strict := flag.Bool("strict", false, "trata avisos como erro (exit 2)")
	skipPat := flag.String("skip", "404.html,tags/index.html,categories/index.html,archives/index.html,2026/index.html", "padrões relativos a -dir (filepath.Match), separados por vírgula; vazio = não ignora")
	postsOnly := flag.Bool("posts-only", false, "só valida HTML em rotas .../AAAA/MM/DD/.../index.html (artigos datados)")
	flag.Parse()

	st, err := os.Stat(*dir)
	if err != nil || !st.IsDir() {
		fmt.Fprintf(os.Stderr, "pasta inválida ou inexistente: %q (rode `hugo` antes)\n", *dir)
		os.Exit(1)
	}

	var files []string
	_ = filepath.Walk(*dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(path), ".html") {
			files = append(files, path)
		}
		return nil
	})

	var skip []string
	for _, p := range strings.Split(*skipPat, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			skip = append(skip, p)
		}
	}

	filtered := files[:0]
	for _, f := range files {
		rel, _ := filepath.Rel(*dir, f)
		rel = filepath.ToSlash(rel)
		if *postsOnly && !isDatedPostPath(rel) {
			continue
		}
		if skipped(rel, skip) {
			continue
		}
		filtered = append(filtered, f)
	}
	files = filtered

	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "nenhum .html após filtros em %q\n", *dir)
		os.Exit(1)
	}

	errCount, warnCount := 0, 0
	for _, f := range files {
		rel, _ := filepath.Rel(*dir, f)
		fs := validateFile(f)
		for _, x := range fs {
			prefix := "  ✓"
			switch x.sev {
			case sevWarn:
				prefix = "  ⚠"
				warnCount++
			case sevErr:
				prefix = "  ✗"
				errCount++
			default:
				continue
			}
			if x.sev != sevOK {
				fmt.Printf("%s [%s] %s\n", prefix, rel, x.msg)
			}
		}
	}

	fmt.Printf("\nArquivos: %d | erros: %d | avisos: %d\n", len(files), errCount, warnCount)
	switch {
	case errCount > 0:
		os.Exit(1)
	case *strict && warnCount > 0:
		os.Exit(2)
	default:
		os.Exit(0)
	}
}

func isDatedPostPath(rel string) bool {
	// ex.: 2026/04/03/slug/index.html
	ok, _ := filepath.Match("[0-9][0-9][0-9][0-9]/[0-9][0-9]/[0-9][0-9]/*/index.html", rel)
	return ok
}

func skipped(rel string, patterns []string) bool {
	for _, pat := range patterns {
		if ok, _ := filepath.Match(pat, rel); ok {
			return true
		}
	}
	return false
}

func validateFile(path string) []finding {
	f, err := os.Open(path)
	if err != nil {
		return []finding{{sevErr, "abrir: " + err.Error()}}
	}
	defer f.Close()

	doc, err := html.Parse(f)
	if err != nil {
		return []finding{{sevErr, "parse HTML: " + err.Error()}}
	}

	title := textContent(findFirst(doc, isTitle))
	meta := collectMeta(doc)
	canonical := firstAttr(findFirst(doc, isCanonical), "href")
	h1s := collectAll(doc, isH1)
	jsonLDs := collectJSONLD(doc)

	var out []finding
	add := func(s severity, msg string) {
		out = append(out, finding{s, msg})
	}

	// ── SEO ──
	if strings.TrimSpace(title) == "" {
		add(sevErr, "SEO: <title> vazio")
	} else {
		n := len([]rune(title))
		if n < 15 {
			add(sevWarn, fmt.Sprintf("SEO: <title> curto (%d runes; sugere-se ≥15)", n))
		}
		if n > 70 {
			add(sevWarn, fmt.Sprintf("SEO: <title> longo (%d runes; sugere-se ≤70)", n))
		}
	}

	desc := meta["name:description"]
	if desc == "" {
		add(sevErr, "SEO: falta <meta name=\"description\">")
	} else {
		n := len([]rune(desc))
		if n < 70 {
			add(sevWarn, fmt.Sprintf("SEO: meta description curta (%d runes; sugere-se ~120–160)", n))
		}
		if n > 320 {
			add(sevWarn, fmt.Sprintf("SEO: meta description muito longa (%d runes)", n))
		}
	}

	if canonical == "" {
		add(sevWarn, "SEO: falta <link rel=\"canonical\">")
	}

	for _, prop := range []string{"og:title", "og:description", "og:url"} {
		if meta["property:"+prop] == "" {
			add(sevErr, "SEO: falta meta property="+prop)
		}
	}
	if meta["property:og:image"] == "" {
		add(sevWarn, "SEO/GEO: falta og:image (compartilhamento e sinais de página)")
	}

	if meta["name:twitter:card"] == "" {
		add(sevWarn, "SEO: falta twitter:card")
	}

	lang := firstAttr(findFirst(doc, isHTML), "lang")
	if lang == "" {
		add(sevWarn, "SEO: falta atributo lang no <html>")
	}

	nh1 := len(h1s)
	switch {
	case nh1 == 0:
		add(sevWarn, "SEO: nenhum <h1> (recomendado 1 por página)")
	case nh1 > 1:
		add(sevWarn, fmt.Sprintf("SEO: %d <h1> (recomendado 1)", nh1))
	}

	// ── Schema / GEO / AIO ──
	types := schemaTypes(jsonLDs)
	hasBlog := types["BlogPosting"] || types["Article"]
	hasWeb := types["WebSite"]
	hasBread := types["BreadcrumbList"]
	hasSpeak := types["SpeakableSpecification"]

	if !hasBlog && !hasWeb && len(jsonLDs) > 0 {
		add(sevWarn, "GEO: JSON-LD sem WebSite/BlogPosting típico")
	}
	if len(jsonLDs) == 0 {
		add(sevErr, "GEO/AIO: nenhum <script type=\"application/ld+json\">")
	}

	if hasBlog {
		if !jsonLDHasAuthor(jsonLDs) {
			add(sevErr, "GEO: BlogPosting sem author (E-E-A-T)")
		}
		if !hasBread {
			add(sevWarn, "GEO: falta BreadcrumbList no JSON-LD")
		}
		if !hasSpeak {
			add(sevWarn, "AIO/AEO: falta SpeakableSpecification (citabilidade em assistentes)")
		}
		if meta["name:keywords"] == "" && !metaHasArticleTag(meta) {
			add(sevWarn, "GEO: sem keywords meta nem article:tag (tópicos/termos)")
		}
	}

	return out
}

func metaHasArticleTag(meta map[string]string) bool {
	for k := range meta {
		if strings.HasPrefix(k, "property:article:") && k != "property:article:published_time" &&
			k != "property:article:modified_time" && k != "property:article:author" {
			return true
		}
	}
	return strings.Contains(strings.Join(keysOf(meta), " "), "article:tag")
}

func keysOf(m map[string]string) []string {
	s := make([]string, 0, len(m))
	for k := range m {
		s = append(s, k)
	}
	return s
}

func jsonLDHasAuthor(jsonLDs []map[string]any) bool {
	for _, o := range jsonLDs {
		if !schemaIsType(o, "BlogPosting") && !schemaIsType(o, "Article") {
			continue
		}
		a, ok := o["author"]
		return ok && a != nil
	}
	return true
}

func schemaIsType(o map[string]any, want string) bool {
	switch t := o["@type"].(type) {
	case string:
		return t == want
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

func schemaTypes(jsonLDs []map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, o := range jsonLDs {
		switch t := o["@type"].(type) {
		case string:
			out[t] = true
		case []any:
			for _, x := range t {
				if s, ok := x.(string); ok {
					out[s] = true
				}
			}
		}
	}
	return out
}

func collectJSONLD(n *html.Node) []map[string]any {
	var out []map[string]any
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "script" {
			t := attr(node, "type")
			if t == "application/ld+json" {
				raw := textChildren(node)
				raw = strings.TrimSpace(raw)
				if raw == "" {
					return
				}
				var one map[string]any
				if err := json.Unmarshal([]byte(raw), &one); err == nil {
					out = append(out, one)
					return
				}
				var arr []map[string]any
				if err := json.Unmarshal([]byte(raw), &arr); err == nil {
					out = append(out, arr...)
				}
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func textChildren(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return b.String()
}

func collectMeta(n *html.Node) map[string]string {
	out := map[string]string{}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "meta" {
			if name := attr(node, "name"); name != "" {
				out["name:"+strings.ToLower(name)] = attr(node, "content")
			}
			if prop := attr(node, "property"); prop != "" {
				out["property:"+strings.ToLower(prop)] = attr(node, "content")
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func findFirst(n *html.Node, pred func(*html.Node) bool) *html.Node {
	if pred(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if x := findFirst(c, pred); x != nil {
			return x
		}
	}
	return nil
}

func collectAll(n *html.Node, pred func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if pred(node) {
			out = append(out, node)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func isTitle(n *html.Node) bool {
	return n.Type == html.ElementNode && n.Data == "title"
}

func isHTML(n *html.Node) bool {
	return n.Type == html.ElementNode && n.Data == "html"
}

func isH1(n *html.Node) bool {
	return n.Type == html.ElementNode && n.Data == "h1"
}

func isCanonical(n *html.Node) bool {
	return n.Type == html.ElementNode && n.Data == "link" &&
		strings.EqualFold(attr(n, "rel"), "canonical")
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func firstAttr(n *html.Node, key string) string {
	if n == nil {
		return ""
	}
	return attr(n, key)
}

func textContent(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}
