package httpcall

import (
	"net/url"
	"sort"
	"strings"
)

func (r *Request) Curl() string {
	r.Init()

	var buf strings.Builder
	buf.WriteString("curl")
	buf.WriteString(" -i")
	if r.HTTPRequest.Method != "GET" {
		buf.WriteString(" -X")
		buf.WriteString(r.HTTPRequest.Method)
	}
	keys := make([]string, 0, len(r.HTTPRequest.Header))
	for k := range r.HTTPRequest.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range r.HTTPRequest.Header[k] {
			buf.WriteString(" -H ")
			buf.WriteString(ShellQuote(k + ": " + v))
		}
	}
	if r.Input == nil && len(r.RawRequestBody) > 0 {
		buf.WriteString(" -d ")
		buf.WriteString(ShellQuote(string(r.RawRequestBody)))
	} else if bodyValues, ok := r.Input.(url.Values); ok {
		for k, vv := range bodyValues {
			for _, v := range vv {
				buf.WriteString(" -d ")
				buf.WriteString(ShellQuote(k + "=" + v))
			}
		}
	} else if len(r.RawRequestBody) > 0 {
		buf.WriteString(" -d ")
		buf.WriteString(ShellQuote(string(r.RawRequestBody)))
	}
	buf.WriteString(" ")
	buf.WriteString(ShellQuote(r.HTTPRequest.URL.String()))
	return buf.String()
}

func ShellQuote(source string) string {
	const specialChars = "\\'\"`${[|&;<>()*?! \t\n~"
	const specialInDouble = "$\\\"!"

	var buf strings.Builder
	buf.Grow(len(source) + 10)

	// pick quotation style, preferring single quotes
	if !strings.ContainsAny(source, specialChars) {
		buf.WriteString(source)
	} else if !strings.ContainsRune(source, '\'') {
		buf.WriteByte('\'')
		buf.WriteString(source)
		buf.WriteByte('\'')
	} else {
		buf.WriteByte('"')
		for {
			i := strings.IndexAny(source, specialInDouble)
			if i < 0 {
				break
			}
			buf.WriteString(source[:i])
			buf.WriteByte('\\')
			buf.WriteByte(source[i])
			source = source[i+1:]
		}
		buf.WriteString(source)
		buf.WriteByte('"')
	}
	return buf.String()
}
