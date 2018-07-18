// Package css minifies CSS3 following the specifications at http://www.w3.org/TR/css-syntax-3/.
package css // import "github.com/tdewolff/minify/css"

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"

	"github.com/tdewolff/minify"
	"github.com/tdewolff/parse"
	"github.com/tdewolff/parse/css"
)

var (
	spaceBytes          = []byte(" ")
	colonBytes          = []byte(":")
	semicolonBytes      = []byte(";")
	commaBytes          = []byte(",")
	leftBracketBytes    = []byte("{")
	rightBracketBytes   = []byte("}")
	zeroBytes           = []byte("0")
	backgroundNoneBytes = []byte("0 0")
)

type cssMinifier struct {
	m *minify.M
	w io.Writer
	p *css.Parser
	o *Minifier

	valuesBuffer []Token
}

////////////////////////////////////////////////////////////////

// DefaultMinifier is the default minifier.
var DefaultMinifier = &Minifier{Decimals: -1, KeepCSS2: false}

// Minifier is a CSS minifier.
type Minifier struct {
	Decimals int
	KeepCSS2 bool
}

// Minify minifies CSS data, it reads from r and writes to w.
func Minify(m *minify.M, w io.Writer, r io.Reader, params map[string]string) error {
	return DefaultMinifier.Minify(m, w, r, params)
}

// Minify minifies CSS data, it reads from r and writes to w.
func (o *Minifier) Minify(m *minify.M, w io.Writer, r io.Reader, params map[string]string) error {
	isInline := params != nil && params["inline"] == "1"
	c := &cssMinifier{
		m: m,
		w: w,
		p: css.NewParser(r, isInline),
		o: o,
	}
	defer c.p.Restore()

	if err := c.minifyGrammar(); err != nil && err != io.EOF {
		return err
	}
	return nil
}

func (c *cssMinifier) minifyGrammar() error {
	semicolonQueued := false
	for {
		gt, _, data := c.p.Next()
		if gt == css.ErrorGrammar {
			if perr, ok := c.p.Err().(*parse.Error); ok && perr.Message == "unexpected token in declaration" {
				if semicolonQueued {
					if _, err := c.w.Write(semicolonBytes); err != nil {
						return err
					}
				}

				// write out the offending declaration
				if _, err := c.w.Write(data); err != nil {
					return err
				}
				vals := c.p.Values()
				if len(vals) > 0 && vals[len(vals)-1].TokenType == css.SemicolonToken {
					vals = vals[:len(vals)-1]
					semicolonQueued = true
				}
				for _, val := range vals {
					if _, err := c.w.Write(val.Data); err != nil {
						return err
					}
				}
				continue
			} else {
				return c.p.Err()
			}
		} else if gt == css.EndAtRuleGrammar || gt == css.EndRulesetGrammar {
			if _, err := c.w.Write(rightBracketBytes); err != nil {
				return err
			}
			semicolonQueued = false
			continue
		}

		if semicolonQueued {
			if _, err := c.w.Write(semicolonBytes); err != nil {
				return err
			}
			semicolonQueued = false
		}

		if gt == css.AtRuleGrammar {
			if _, err := c.w.Write(data); err != nil {
				return err
			}
			values := c.p.Values()
			if css.ToHash(data[1:]) == css.Import && len(values) == 2 && values[1].TokenType == css.URLToken {
				url := values[1].Data
				if url[4] != '"' && url[4] != '\'' {
					url = url[3:]
					url[0] = '"'
					url[len(url)-1] = '"'
				} else {
					url = url[4 : len(url)-1]
				}
				values[1].Data = url
			}
			for _, val := range values {
				if _, err := c.w.Write(val.Data); err != nil {
					return err
				}
			}
			semicolonQueued = true
		} else if gt == css.BeginAtRuleGrammar {
			if _, err := c.w.Write(data); err != nil {
				return err
			}
			for _, val := range c.p.Values() {
				if _, err := c.w.Write(val.Data); err != nil {
					return err
				}
			}
			if _, err := c.w.Write(leftBracketBytes); err != nil {
				return err
			}
		} else if gt == css.QualifiedRuleGrammar {
			if err := c.minifySelectors(data, c.p.Values()); err != nil {
				return err
			}
			if _, err := c.w.Write(commaBytes); err != nil {
				return err
			}
		} else if gt == css.BeginRulesetGrammar {
			if err := c.minifySelectors(data, c.p.Values()); err != nil {
				return err
			}
			if _, err := c.w.Write(leftBracketBytes); err != nil {
				return err
			}
		} else if gt == css.DeclarationGrammar {
			if _, err := c.w.Write(data); err != nil {
				return err
			}
			if _, err := c.w.Write(colonBytes); err != nil {
				return err
			}
			if err := c.minifyDeclaration(data, c.p.Values()); err != nil {
				return err
			}
			semicolonQueued = true
		} else if gt == css.CustomPropertyGrammar {
			if _, err := c.w.Write(data); err != nil {
				return err
			}
			if _, err := c.w.Write(colonBytes); err != nil {
				return err
			}
			if _, err := c.w.Write(c.p.Values()[0].Data); err != nil {
				return err
			}
			semicolonQueued = true
		} else if gt == css.CommentGrammar {
			if len(data) > 5 && data[1] == '*' && data[2] == '!' {
				if _, err := c.w.Write(data[:3]); err != nil {
					return err
				}
				comment := parse.TrimWhitespace(parse.ReplaceMultipleWhitespace(data[3 : len(data)-2]))
				if _, err := c.w.Write(comment); err != nil {
					return err
				}
				if _, err := c.w.Write(data[len(data)-2:]); err != nil {
					return err
				}
			}
		} else if _, err := c.w.Write(data); err != nil {
			return err
		}
	}
}

func (c *cssMinifier) minifySelectors(property []byte, values []css.Token) error {
	inAttr := false
	isClass := false
	for _, val := range c.p.Values() {
		if !inAttr {
			if val.TokenType == css.IdentToken {
				if !isClass {
					parse.ToLower(val.Data)
				}
				isClass = false
			} else if val.TokenType == css.DelimToken && val.Data[0] == '.' {
				isClass = true
			} else if val.TokenType == css.LeftBracketToken {
				inAttr = true
			}
		} else {
			if val.TokenType == css.StringToken && len(val.Data) > 2 {
				s := val.Data[1 : len(val.Data)-1]
				if css.IsIdent([]byte(s)) {
					if _, err := c.w.Write(s); err != nil {
						return err
					}
					continue
				}
			} else if val.TokenType == css.RightBracketToken {
				inAttr = false
			}
		}
		if _, err := c.w.Write(val.Data); err != nil {
			return err
		}
	}
	return nil
}

type Token struct {
	css.TokenType
	Data       []byte
	Components []css.Token // only filled for functions
}

func (t Token) String() string {
	if len(t.Components) == 0 {
		return t.TokenType.String() + "(" + string(t.Data) + ")"
	} else {
		return fmt.Sprint(t.Components)
	}
}

func (a Token) Equal(b Token) bool {
	if a.TokenType == b.TokenType && bytes.Equal(a.Data, b.Data) && len(a.Components) == len(b.Components) {
		for i := 0; i < len(a.Components); i++ {
			if a.Components[i].TokenType != b.Components[i].TokenType || !bytes.Equal(a.Components[i].Data, b.Components[i].Data) {
				return false
			}
		}
		return true
	}
	return false
}

func (c *cssMinifier) minifyDeclaration(property []byte, components []css.Token) error {
	if len(components) == 0 {
		return nil
	}

	// Strip !important from the component list, this will be added later separately
	important := false
	if len(components) > 2 && components[len(components)-2].TokenType == css.DelimToken && components[len(components)-2].Data[0] == '!' && css.ToHash(components[len(components)-1].Data) == css.Important {
		components = components[:len(components)-2]
		important = true
	}

	// Check if this is a simple list of values separated by whitespace or commas, otherwise we'll not be processing
	simple := true
	prevSep := true
	values := c.valuesBuffer[:0]
	for i := 0; i < len(components); i++ {
		comp := components[i]
		if comp.TokenType == css.LeftParenthesisToken || comp.TokenType == css.LeftBraceToken || comp.TokenType == css.LeftBracketToken || comp.TokenType == css.RightParenthesisToken || comp.TokenType == css.RightBraceToken || comp.TokenType == css.RightBracketToken {
			simple = false
			break
		}

		if !prevSep && comp.TokenType != css.WhitespaceToken && comp.TokenType != css.CommaToken && (comp.TokenType != css.DelimToken || comp.Data[0] != '/') {
			simple = false
			break
		}

		if comp.TokenType == css.WhitespaceToken || comp.TokenType == css.CommaToken || comp.TokenType == css.DelimToken && comp.Data[0] == '/' {
			prevSep = true
			if comp.TokenType != css.WhitespaceToken {
				values = append(values, Token{comp.TokenType, comp.Data, nil})
			}
		} else if comp.TokenType == css.FunctionToken {
			prevSep = false
			j := i + 1
			level := 0
			for ; j < len(components); j++ {
				if components[j].TokenType == css.LeftParenthesisToken {
					level++
				} else if components[j].TokenType == css.RightParenthesisToken {
					if level == 0 {
						j++
						break
					}
					level--
				}
			}
			values = append(values, Token{components[i].TokenType, components[i].Data, components[i:j]})
			i = j - 1
		} else {
			prevSep = false
			values = append(values, Token{components[i].TokenType, components[i].Data, nil})
		}
	}
	c.valuesBuffer = values

	prop := css.ToHash(property)
	// Do not process complex values (eg. containing blocks or is not alternated between whitespace/commas and flat values
	if !simple {
		if prop == css.Filter && len(components) == 11 {
			if bytes.Equal(components[0].Data, []byte("progid")) &&
				components[1].TokenType == css.ColonToken &&
				bytes.Equal(components[2].Data, []byte("DXImageTransform")) &&
				components[3].Data[0] == '.' &&
				bytes.Equal(components[4].Data, []byte("Microsoft")) &&
				components[5].Data[0] == '.' &&
				bytes.Equal(components[6].Data, []byte("Alpha(")) &&
				bytes.Equal(parse.ToLower(components[7].Data), []byte("opacity")) &&
				components[8].Data[0] == '=' &&
				components[10].Data[0] == ')' {
				components = components[6:]
				components[0].Data = []byte("alpha(")
			}
		}

		for _, component := range components {
			if _, err := c.w.Write(component.Data); err != nil {
				return err
			}
		}
		if important {
			if _, err := c.w.Write([]byte("!important")); err != nil {
				return err
			}
		}
		return nil
	}

	for i := range values {
		values[i].TokenType, values[i].Data = c.shortenToken(prop, values[i].TokenType, values[i].Data)
	}
	if len(values) > 0 {
		values = c.minifyProperty(prop, values)
	}

	prevSep = true
	for _, value := range values {
		if !prevSep && value.TokenType != css.CommaToken && (value.TokenType != css.DelimToken || value.Data[0] != '/') {
			if _, err := c.w.Write([]byte(" ")); err != nil {
				return err
			}
		}

		if value.TokenType == css.FunctionToken {
			err := c.minifyFunction(value.Components)
			if err != nil {
				return err
			}
		} else {
			if _, err := c.w.Write(value.Data); err != nil {
				return err
			}
		}

		if value.TokenType == css.CommaToken || value.TokenType == css.DelimToken && value.Data[0] == '/' {
			prevSep = true
		} else {
			prevSep = false
		}
	}

	if important {
		if _, err := c.w.Write([]byte("!important")); err != nil {
			return err
		}
	}
	return nil
}

func (c *cssMinifier) minifyProperty(prop css.Hash, values []Token) []Token {
	switch prop {
	case css.Font:
		if len(values) > 1 {
			i := len(values)
			for j, value := range values[2:] {
				if value.TokenType == css.CommaToken {
					i = 2 + j - 1 // identifier before first comma is a font-family
					break
				}
			}

			i--
			for ; i > 0; i-- { // i cannot be 0, font-family must be prepended by font-size
				if values[i-1].TokenType == css.DelimToken && values[i-1].Data[0] == '/' {
					break
				} else if values[i].TokenType != css.IdentToken && values[i].TokenType != css.StringToken {
					break
				} else if values[i].TokenType == css.IdentToken {
					h := css.ToHash(values[i].Data)
					// inherit, initial and unset are followed by an IdentToken/StringToken, so must be for font-size
					if h == css.Xx_Small || h == css.X_Small || h == css.Small || h == css.Medium || h == css.Large || h == css.X_Large || h == css.Xx_Large || h == css.Smaller || h == css.Larger || h == css.Inherit || h == css.Initial || h == css.Unset {
						break
					}
				}
			}

			// font-family minified in place
			values = append(values[:i+1], c.minifyProperty(css.Font_Family, values[i+1:])...)

			if i > 0 {
				// line-height
				if i > 1 && values[i-1].TokenType == css.DelimToken && values[i-1].Data[0] == '/' {
					if values[i].TokenType == css.IdentToken && bytes.Equal(values[i].Data, []byte("normal")) {
						values = append(values[:i-1], values[i+1:]...)
					}
					i -= 2
				}

				// font-size
				i--

				for ; i > -1; i-- {
					if values[i].TokenType == css.IdentToken {
						val := css.ToHash(values[i].Data)
						if val == css.Normal {
							values = append(values[:i], values[i+1:]...)
						} else if val == css.Bold {
							values[i].TokenType = css.NumberToken
							values[i].Data = []byte("700")
						}
					} else if values[i].TokenType == css.NumberToken && bytes.Equal(values[i].Data, []byte("400")) {
						values = append(values[:i], values[i+1:]...)
                    }
				}
			}
		}
	case css.Font_Family:
		for i, value := range values {
			if value.TokenType == css.StringToken && len(value.Data) > 2 {
				unquote := true
				parse.ToLower(value.Data)
				s := value.Data[1 : len(value.Data)-1]
				if len(s) > 0 {
					for _, split := range bytes.Split(s, spaceBytes) {
						// if len is zero, it contains two consecutive spaces
						if len(split) == 0 || !css.IsIdent(split) {
							unquote = false
							break
						}
					}
				}
				if unquote {
					values[i].Data = s
				}
			}
		}
	case css.Font_Weight:
		if len(values) == 1 && values[0].TokenType == css.IdentToken {
			val := css.ToHash(values[0].Data)
			if prop == css.Font_Weight && val == css.Normal {
				values[0].TokenType = css.NumberToken
				values[0].Data = []byte("400")
			} else if val == css.Bold {
				values[0].TokenType = css.NumberToken
				values[0].Data = []byte("700")
			}
		}
	case css.Margin, css.Padding, css.Border_Width:
		n := len(values)
		if n == 2 {
			if values[0].Equal(values[1]) {
				values = values[:1]
			}
		} else if n == 3 {
			if values[0].Equal(values[1]) && values[0].Equal(values[2]) {
				values = values[:1]
			} else if values[0].Equal(values[2]) {
				values = values[:2]
			}
		} else if n == 4 {
			if values[0].Equal(values[1]) && values[0].Equal(values[2]) && values[0].Equal(values[3]) {
				values = values[:1]
			} else if values[0].Equal(values[2]) && values[1].Equal(values[3]) {
				values = values[:2]
			} else if values[1].Equal(values[3]) {
				values = values[:3]
			}
		}
	case css.Outline, css.Border, css.Border_Bottom, css.Border_Left, css.Border_Right, css.Border_Top:
		none := false
		iZero := -1
		for i, value := range values {
			if len(value.Data) == 1 && value.Data[0] == '0' {
				iZero = i
			} else if css.ToHash(value.Data) == css.None {
				values[i].TokenType = css.NumberToken
				values[i].Data = zeroBytes
				none = true
			}
		}
		if none && iZero != -1 {
			values = append(values[:iZero], values[iZero+1:]...)
		}
	case css.Background:
		ident := css.ToHash(values[0].Data)
		if len(values) == 1 && (ident == css.None || bytes.Equal(values[0].Data, []byte("#0000"))) {
			values[0].Data = backgroundNoneBytes
		}
	case css.Box_Shadow:
		if len(values) == 4 && len(values[0].Data) == 1 && values[0].Data[0] == '0' && len(values[1].Data) == 1 && values[1].Data[0] == '0' && len(values[2].Data) == 1 && values[2].Data[0] == '0' && len(values[3].Data) == 1 && values[3].Data[0] == '0' {
			values = values[:2]
		}
    case css.Ms_Filter:
        alpha := []byte("progid:DXImageTransform.Microsoft.Alpha(Opacity=")
        if values[0].TokenType == css.StringToken && bytes.HasPrefix(values[0].Data[1:len(values[0].Data)-1], alpha) {
            values[0].Data = append(append([]byte{values[0].Data[0]}, []byte("alpha(opacity=")...), values[0].Data[1+len(alpha):]...)
        }
	}
	return values
}

func (c *cssMinifier) minifyFunction(values []css.Token) error {
	if n := len(values); n > 2 {
		fun := css.ToHash(values[0].Data[0 : len(values[0].Data)-1])
		if fun == css.Rgb || fun == css.Rgba || fun == css.Hsl || fun == css.Hsla {
			valid := true
			vals := []*css.Token{}
			for i, value := range values[1 : n-1] {
				numeric := value.TokenType == css.NumberToken || value.TokenType == css.PercentageToken
				separator := value.TokenType == css.CommaToken || i != 5 && value.TokenType == css.WhitespaceToken || i == 5 && value.TokenType == css.DelimToken && value.Data[0] == '/'
				if i%2 == 0 && !numeric || i%2 == 1 && !separator {
					valid = false
				} else if numeric {
					vals = append(vals, &values[i+1])
				}
			}

			if valid {
				for _, val := range vals {
					val.TokenType, val.Data = c.shortenToken(0, val.TokenType, val.Data)
				}

				a := byte(255)
				if len(vals) == 4 {
					d, _ := strconv.ParseFloat(string(values[7].Data), 32) // can never fail because if valid == true than this is a NumberToken or PercentageToken
					if d < minify.Epsilon {                                // zero or less
						if _, err := c.w.Write([]byte("#0000")); err != nil { // transparent
							return err
						}
						return nil
					} else if d >= 1.0 { // one or more
						values = values[:7]
						a = 255
					} else {
						a = byte(d*255.0 + 0.5)
					}
				}

				if (fun == css.Rgb || fun == css.Rgba) && (len(vals) == 3 || len(vals) == 4) {
					if !c.o.KeepCSS2 && fun == css.Rgba {
						values[0].Data = []byte("rgb(")
					}

					rgba := [4]byte{}
					rgba[3] = a
					for j, val := range vals[:3] {
						if val.TokenType == css.NumberToken {
							d, _ := strconv.ParseInt(string(val.Data), 10, 32)
							if d < 0 {
								d = 0
							} else if d > 255 {
								d = 255
							}
							rgba[j] = byte(d)
						} else if val.TokenType == css.PercentageToken {
							d, _ := strconv.ParseFloat(string(val.Data[:len(val.Data)-1]), 32)
							if d < 0.0 {
								d = 0.0
							} else if d > 100.0 {
								d = 100.0
							}
							rgba[j] = byte((d / 100.0 * 255.0) + 0.5)
						}
					}

					val := make([]byte, 9)
					val[0] = '#'
					hex.Encode(val[1:], rgba[:])
					parse.ToLower(val)
					if a == 255 {
						if s, ok := ShortenColorHex[string(val[:7])]; ok {
							if _, err := c.w.Write(s); err != nil {
								return err
							}
							return nil
						} else if val[1] == val[2] && val[3] == val[4] && val[5] == val[6] {
							val[2] = val[3]
							val[3] = val[5]
							val = val[:4]
						} else {
							val = val[:7]
						}
					} else if val[1] == val[2] && val[3] == val[4] && val[5] == val[6] && val[7] == val[8] {
						val[2] = val[3]
						val[3] = val[5]
						val[4] = val[7]
						val = val[:5]
					}

					if !c.o.KeepCSS2 || a == 255 {
						if _, err := c.w.Write(val); err != nil {
							return err
						}
						return nil
					}
				} else if (fun == css.Hsl || fun == css.Hsla) && (len(vals) == 3 || len(vals) == 4) {
					if !c.o.KeepCSS2 && fun == css.Hsla {
						values[0].Data = []byte("hsl(")
					}

					if vals[0].TokenType == css.NumberToken && vals[1].TokenType == css.PercentageToken && vals[2].TokenType == css.PercentageToken {
						h, _ := strconv.ParseFloat(string(vals[0].Data), 32)
						s, _ := strconv.ParseFloat(string(vals[1].Data[:len(vals[1].Data)-1]), 32)
						l, _ := strconv.ParseFloat(string(vals[2].Data[:len(vals[2].Data)-1]), 32)
						for h > 360.0 {
							h -= 360.0
						}
						if s < 0.0 {
							s = 0.0
						} else if s > 100.0 {
							s = 100.0
						}
						if l < 0.0 {
							l = 0.0
						} else if l > 100.0 {
							l = 100.0
						}

						r, g, b := css.HSL2RGB(h/360.0, s/100.0, l/100.0)
						rgba := []byte{byte((r * 255.0) + 0.5), byte((g * 255.0) + 0.5), byte((b * 255.0) + 0.5), a}
						val := make([]byte, 9)
						val[0] = '#'
						hex.Encode(val[1:], rgba[:])
						parse.ToLower(val)
						if a == 255 {
							if s, ok := ShortenColorHex[string(val[:7])]; ok {
								if _, err := c.w.Write(s); err != nil {
									return err
								}
								return nil
							} else if val[1] == val[2] && val[3] == val[4] && val[5] == val[6] {
								val[2] = val[3]
								val[3] = val[5]
								val = val[:4]
							} else {
								val = val[:7]
							}
						} else if val[1] == val[2] && val[3] == val[4] && val[5] == val[6] && val[7] == val[8] {
							val[2] = val[3]
							val[3] = val[5]
							val[4] = val[7]
							val = val[:5]
						}

						if !c.o.KeepCSS2 || a == 255 {
							if _, err := c.w.Write(val); err != nil {
								return err
							}
						}
						return nil
					}
				}
			}
		} else if fun == css.Local && n == 3 {
			data := values[1].Data
			if data[0] == '\'' || data[0] == '"' {
				data = removeStringNewlinex(data)
				if css.IsURLUnquoted(data[1 : len(data)-1]) {
					data = data[1 : len(data)-1]
				}
				values[1].Data = data
			}
		}
	}

	for _, value := range values {
		if _, err := c.w.Write(value.Data); err != nil {
			return err
		}
	}
	return nil
}

func (c *cssMinifier) shortenToken(prop css.Hash, tt css.TokenType, data []byte) (css.TokenType, []byte) {
	if tt == css.NumberToken || tt == css.PercentageToken || tt == css.DimensionToken {
		if tt == css.NumberToken && (prop == css.Z_Index || prop == css.Counter_Increment || prop == css.Counter_Reset || prop == css.Orphans || prop == css.Widows) {
			return tt, data // integers
		}
		n := len(data)
		if tt == css.PercentageToken {
			n--
		} else if tt == css.DimensionToken {
			n = parse.Number(data)
		}
		dim := data[n:]
		parse.ToLower(dim)
		if !c.o.KeepCSS2 {
			data = minify.Number(data[:n], c.o.Decimals)
		} else {
			data = minify.Decimal(data[:n], c.o.Decimals) // don't use exponents
		}
		if tt == css.DimensionToken && (len(data) != 1 || data[0] != '0' || !optionalZeroDimension[string(dim)] || prop == css.Flex) {
			data = append(data, dim...)
		} else if tt == css.PercentageToken {
			data = append(data, '%') // TODO: drop percentage for properties that accept <percentage> and <length>
		}
	} else if tt == css.IdentToken {
		parse.ToLower(parse.Copy(data)) // not all identifiers are case-insensitive; all <custom-ident> properties are case-sensitive
		if hex, ok := ShortenColorName[css.ToHash(data)]; ok {
			tt = css.HashToken
			data = hex
		}
	} else if tt == css.HashToken {
		parse.ToLower(data)
		if len(data) == 9 && data[7] == data[8] {
			if data[7] == 'f' {
				data = data[:7]
			} else if data[7] == '0' {
				data = []byte("#0000")
			}
		}
		if ident, ok := ShortenColorHex[string(data)]; ok {
			tt = css.IdentToken
			data = ident
		} else if len(data) == 7 && data[1] == data[2] && data[3] == data[4] && data[5] == data[6] {
			tt = css.HashToken
			data[2] = data[3]
			data[3] = data[5]
			data = data[:4]
		} else if len(data) == 9 && data[1] == data[2] && data[3] == data[4] && data[5] == data[6] && data[7] == data[8] {
			tt = css.HashToken
			data[2] = data[3]
			data[3] = data[5]
			data[4] = data[7]
			data = data[:5]
		}
	} else if tt == css.StringToken {
		data = removeStringNewlinex(data)
	} else if tt == css.URLToken {
		parse.ToLower(data[:3])
		if len(data) > 10 {
			uri := parse.TrimWhitespace(data[4 : len(data)-1])
			delim := byte('"')
			if uri[0] == '\'' || uri[0] == '"' {
				delim = uri[0]
				uri = removeStringNewlinex(uri)
				uri = uri[1 : len(uri)-1]
			}
			uri = minify.DataURI(c.m, uri)
			if css.IsURLUnquoted(uri) {
				data = append(append([]byte("url("), uri...), ')')
			} else {
				data = append(append(append([]byte("url("), delim), uri...), delim, ')')
			}
		}
	}
	return tt, data
}

func removeStringNewlinex(data []byte) []byte {
	// remove any \\\r\n \\\r \\\n
	for i := 1; i < len(data)-2; i++ {
		if data[i] == '\\' && (data[i+1] == '\n' || data[i+1] == '\r') {
			// encountered first replacee, now start to move bytes to the front
			j := i + 2
			if data[i+1] == '\r' && len(data) > i+2 && data[i+2] == '\n' {
				j++
			}
			for ; j < len(data); j++ {
				if data[j] == '\\' && len(data) > j+1 && (data[j+1] == '\n' || data[j+1] == '\r') {
					if data[j+1] == '\r' && len(data) > j+2 && data[j+2] == '\n' {
						j++
					}
					j++
				} else {
					data[i] = data[j]
					i++
				}
			}
			data = data[:i]
			break
		}
	}
	return data
}
