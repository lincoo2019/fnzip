package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

var passwordPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:解压|压缩|提取|访问|文件|默认)?\s*(?:密码|密碼|口令|password|passwd|pwd)\s*(?:是|为|[:：=])\s*["'“”‘’]?([^\s，,。；;、|<>\]】)）]{2,72})`),
	regexp.MustCompile(`(?i)【\s*(?:解压|压缩|提取)?\s*(?:密码|密碼|password)\s*[:：=]?\s*([^】]{2,72})】`),
}

var domainRE = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.)?[a-z0-9][a-z0-9\-]{1,62}\.(?:com|cn|net|org|cc|tv|la|me|info|xyz|top|vip|club|site|online|pw|ws|io|co|us|tw|hk|jp|kr|ru|de|uk|fr|eu|moe|one|live|fun|shop|store|cloud|link|app|dev|pro|biz|in|to|se|nu|lt|pl|br|au|ca|icu|work|art|wang|xin|ren|ink|pub|bar|love|date|men|loan|win|bid|download|stream|science|party|review|trade|racing|accountant|webcam|faith|gift|cricket)\b`)

var tokenSplitRE = regexp.MustCompile(`[\s\[\]【】()（）{}<>《》"'“”‘’_,，;；:：!！?？|/\\]+`)

var keywordRE = regexp.MustCompile(`(?i)密码|密碼|口令|password|passwd|pwd|提取码`)

var genericTokens = map[string]bool{
	"part": true, "rar": true, "zip": true, "7z": true, "tar": true, "gz": true,
	"1080p": true, "720p": true, "2160p": true, "4k": true, "8k": true,
	"x264": true, "x265": true, "h264": true, "h265": true, "hevc": true,
	"aac": true, "dts": true, "ac3": true, "bluray": true, "webrip": true,
	"hdr": true, "remux": true, "mkv": true, "mp4": true, "avi": true,
	"全集": true, "下载": true, "资源": true, "免费": true, "高清": true,
	"完整版": true, "最新": true, "更新": true, "完结": true, "小说": true,
	"电影": true, "电视剧": true, "动漫": true, "合集": true, "压缩包": true,
	"解压密码": true, "password": true, "www": true, "com": true, "cn": true,
	"net": true, "org": true, "http": true, "https": true, "html": true,
}

var builtinPasswords = []string{
	"www.52pojie.cn", "52pojie", "www.52pj.com",
	"www.iopq.net", "www.dygod.net", "www.ygdy8.com", "www.dytt8.net", "www.dytt8899.com",
	"www.66ys.cc", "www.66ys.tv", "www.6vhao.net", "www.6v520.net", "www.80s.la", "www.80s.tw",
	"www.btbtt.me", "www.btbtt12.com", "www.btbtt11.com", "www.btbtt10.com", "www.btbtt.us",
	"www.zimuku.net", "www.zimuku.la", "www.zimuku.org", "www.zmz2019.com", "www.zmz.com",
	"www.zhuixinfan.com", "www.mp4ba.com", "www.hdchina.org", "www.chdtv.net", "www.hdbits.org",
	"www.3dmgame.com", "bbs.3dmgame.com", "www.ali213.net", "www.gamersky.com", "www.9game.cn",
	"www.pcbeta.com", "www.xiazaiba.com", "www.cr173.com", "www.downxia.com", "www.jb51.net",
	"www.51cto.com", "www.it168.com", "www.ruanmei.com", "www.win7china.com", "www.xitongzhijia.net",
	"www.ghostxp.net", "www.dhghost.com", "www.newasp.net", "www.greenxf.com", "www.ddooo.com",
	"www.hao123.com", "www.baidu.com", "www.qq.com", "www.360.cn", "www.taobao.com",
	"123456", "12345678", "123456789", "1234", "12345", "0000", "111111", "888888", "666666",
	"password", "admin", "root", "abc123",
}

func buildCandidates(archivePath string, info *ArchiveInfo, settings Settings, learned []PasswordRecord) []string {
	set := map[string]bool{}
	var out []string
	add := func(s string) {
		s = cleanCandidate(s)
		if s == "" || set[s] {
			return
		}
		if len(s) < 2 || len(s) > 128 {
			return
		}
		if strings.ContainsAny(s, "\r\n\t") {
			return
		}
		set[s] = true
		out = append(out, s)
	}

	addAll := func(list []string) {
		for _, s := range list {
			add(s)
		}
	}

	nameText := archivePath + " " + filepath.Base(filepath.Dir(archivePath))
	addAll(extractExplicit(nameText))
	if info != nil && info.Comment != "" {
		addAll(extractExplicit(info.Comment))
		addAll(extractDomains(info.Comment))
	}

	adjText, adjDomains, adjTokens := readAdjacentTexts(archivePath)
	addAll(extractExplicit(adjText))

	for _, pw := range settings.PasswordBook {
		add(pw)
	}

	learnedSorted := append([]PasswordRecord(nil), learned...)
	sort.Slice(learnedSorted, func(i, j int) bool {
		if learnedSorted[i].Hits != learnedSorted[j].Hits {
			return learnedSorted[i].Hits > learnedSorted[j].Hits
		}
		return learnedSorted[i].LastUsed.After(learnedSorted[j].LastUsed)
	})
	for _, r := range learnedSorted {
		add(r.Password)
	}

	addAll(adjTokens)
	addAll(extractDomains(nameText))
	addAll(adjDomains)
	addAll(tokenizeNames(archivePath))

	for _, pw := range builtinPasswords {
		add(pw)
	}

	max := settings.MaxPasswordTries
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

func extractExplicit(text string) []string {
	var out []string
	for _, re := range passwordPatterns {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if len(m) > 1 {
				out = append(out, m[1])
			}
		}
	}
	return out
}

func extractDomains(text string) []string {
	var out []string
	for _, m := range domainRE.FindAllString(text, -1) {
		v := strings.TrimSpace(m)
		v = strings.TrimSuffix(v, "/")
		out = append(out, v)
		noScheme := strings.TrimPrefix(strings.TrimPrefix(v, "http://"), "https://")
		out = append(out, noScheme)
		if !strings.HasPrefix(strings.ToLower(noScheme), "www.") {
			out = append(out, "www."+noScheme)
		} else {
			out = append(out, strings.TrimPrefix(noScheme, "www."))
		}
	}
	return out
}

func tokenizeNames(archivePath string) []string {
	var out []string
	base := archiveBaseName(archivePath)
	out = append(out, base)
	parent := filepath.Base(filepath.Dir(archivePath))
	grand := filepath.Base(filepath.Dir(filepath.Dir(archivePath)))
	out = append(out, parent, grand)

	text := base + " " + parent + " " + grand
	for _, tok := range tokenSplitRE.Split(text, -1) {
		tok = strings.TrimSpace(tok)
		if tok != "" {
			out = append(out, tok)
			if strings.ContainsAny(tok, "-.") {
				for _, sub := range regexp.MustCompile(`[-.]`).Split(tok, -1) {
					if len(sub) >= 3 {
						out = append(out, sub)
					}
				}
			}
		}
	}
	return out
}

func readAdjacentTexts(archivePath string) (string, []string, []string) {
	dir := filepath.Dir(archivePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, nil
	}
	allowed := map[string]bool{
		".txt": true, ".nfo": true, ".url": true, ".html": true, ".htm": true,
		".md": true, ".log": true, ".ini": true, ".info": true, ".说明": true,
	}
	var textAll strings.Builder
	var domains []string
	var tokens []string
	count := 0
	for _, e := range entries {
		if count >= 24 {
			break
		}
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.EqualFold(filepath.Join(dir, name), archivePath) {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !allowed[ext] {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() > 1024*1024 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if len(data) > 256*1024 {
			data = data[:256*1024]
		}
		content := decodeText(data)
		textAll.WriteString("\n")
		textAll.WriteString(content)
		domains = append(domains, extractDomains(content)...)
		for _, line := range strings.Split(content, "\n") {
			if !keywordRE.MatchString(line) {
				continue
			}
			line = strings.TrimSpace(line)
			for _, tok := range tokenSplitRE.Split(line, -1) {
				tok = cleanCandidate(tok)
				if len(tok) >= 3 && len(tok) <= 64 {
					tokens = append(tokens, tok)
				}
			}
			for _, m := range domainRE.FindAllString(line, -1) {
				tokens = append(tokens, cleanCandidate(m))
			}
		}
		count++
	}
	return textAll.String(), domains, tokens
}

func decodeText(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	if decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data); err == nil {
		return string(decoded)
	}
	return string(data)
}

func parsePasswordLines(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimPrefix(strings.TrimSuffix(line, "\r"), "\ufeff")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out
}

func cleanCandidate(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, " \t\r\n\"'“”‘’`[]【】()（）<>《》,，。;；:：、|")
	if s == "" {
		return ""
	}
	for _, suffix := range []string{"解压密码", "解压", "密码", "提取码"} {
		if strings.HasSuffix(s, suffix) && len(s) > len(suffix) {
			s = strings.TrimSuffix(s, suffix)
		}
	}
	if genericTokens[strings.ToLower(s)] {
		return ""
	}
	return strings.TrimSpace(s)
}
