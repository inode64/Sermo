package checks

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/hostfs"
	"sermo/internal/units"
)

// Native config formats complement application-specific command validators.
const (
	CheckKeyFormat             = "format"
	ConfigFormatXML            = "xml"
	ConfigFormatJAR            = "jar"
	configXMLMaxBytes          = 16 * units.BytesPerMiB
	configXMLMaxDepth          = 256
	xmlNamespaceDeclaration    = "xmlns"
	xmlNamespaceDeclarationURI = "http://www.w3.org/2000/xmlns/"
	xmlNamespacePrefix         = "xml"
	xmlNamespaceURI            = "http://www.w3.org/XML/1998/namespace"
)

var errConfigSyntax = errors.New("invalid configuration")

// ValidateConfigFormat rejects ambiguous combinations before any file is read.
func ValidateConfigFormat(entry map[string]any) error {
	raw, present := entry[CheckKeyFormat]
	if !present {
		return nil
	}
	format := cfgval.AsString(raw)
	if format != ConfigFormatXML && format != ConfigFormatJAR {
		return errors.New("format must be xml or jar")
	}
	if _, command := entry[CheckKeyCommand]; command {
		return errors.New("format and command are mutually exclusive")
	}
	paths := cfgval.StringList(entry[CheckKeyPath])
	if len(paths) == 0 {
		return errors.New("format requires path")
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("native config paths must be absolute and clean")
		}
	}
	return nil
}

func validateNativeConfig(ctx context.Context, format string, paths []string) ([]string, error) {
	if format == ConfigFormatJAR {
		return inspectJavaArchives(ctx, paths, openTrustedJavaArchive)
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("validate config: %w", err)
		}
		data, err := hostfs.ReadRegularFile(path, configXMLMaxBytes+1)
		if err != nil {
			return nil, fmt.Errorf("read XML %s: %w", path, err)
		}
		if len(data) > configXMLMaxBytes {
			return nil, fmt.Errorf("%w: XML %s exceeds size limit", errConfigSyntax, path)
		}
		if err := validateXML(ctx, data); err != nil {
			return nil, fmt.Errorf("XML %s: %w", path, err)
		}
	}
	return paths, nil
}

type xmlConfigElement struct {
	name       xml.Name
	namespaces map[string]string
}

func validateXML(ctx context.Context, data []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var stack []xmlConfigElement
	roots := 0
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("validate XML: %w", err)
		}
		offset := decoder.InputOffset()
		token, err := decoder.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %w", errConfigSyntax, err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			if len(stack) == 0 {
				roots++
			}
			element, err := newXMLConfigElement(token, stack)
			if err != nil {
				return err
			}
			stack = append(stack, element)
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1].name != token.Name {
				return fmt.Errorf("%w: mismatched XML closing element", errConfigSyntax)
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 && strings.TrimSpace(string(token)) != "" {
				return fmt.Errorf("%w: text outside XML root", errConfigSyntax)
			}
		case xml.ProcInst:
			if err := validateXMLInstruction(token, offset); err != nil {
				return err
			}
		case xml.Directive:
			// DTD/entity expansion is not needed by application config and must never
			// fetch external resources or turn a bounded file into unbounded work.
			return fmt.Errorf("%w: XML directives are not supported", errConfigSyntax)
		}
	}
	if roots != 1 || len(stack) != 0 {
		return fmt.Errorf("%w: XML requires one complete root", errConfigSyntax)
	}
	return nil
}

func newXMLConfigElement(token xml.StartElement, stack []xmlConfigElement) (xmlConfigElement, error) {
	if len(stack) >= configXMLMaxDepth {
		return xmlConfigElement{}, fmt.Errorf("%w: XML nesting exceeds limit", errConfigSyntax)
	}
	namespaces := map[string]string{xmlNamespacePrefix: xmlNamespaceURI}
	if len(stack) > 0 {
		namespaces = maps.Clone(stack[len(stack)-1].namespaces)
	}
	if err := validateXMLStart(token, namespaces); err != nil {
		return xmlConfigElement{}, err
	}
	return xmlConfigElement{name: token.Name, namespaces: namespaces}, nil
}

func validateXMLStart(element xml.StartElement, namespaces map[string]string) error {
	declarations := make(map[string]bool)
	for _, attr := range element.Attr {
		prefix := ""
		if attr.Name.Space == xmlNamespaceDeclaration {
			prefix = attr.Name.Local
		} else if attr.Name.Space != "" || attr.Name.Local != xmlNamespaceDeclaration {
			continue
		}
		if declarations[prefix] || prefix == xmlNamespaceDeclaration || attr.Value == xmlNamespaceDeclarationURI || (prefix != "" && attr.Value == "") || (prefix == xmlNamespacePrefix && attr.Value != xmlNamespaceURI) || (prefix != xmlNamespacePrefix && attr.Value == xmlNamespaceURI) {
			return fmt.Errorf("%w: invalid XML namespace declaration", errConfigSyntax)
		}
		declarations[prefix] = true
		namespaces[prefix] = attr.Value
	}
	if element.Name.Space != "" && namespaces[element.Name.Space] == "" {
		return fmt.Errorf("%w: undeclared XML element prefix", errConfigSyntax)
	}
	attrs := make(map[xml.Name]bool)
	for _, attr := range element.Attr {
		if attr.Name.Space == xmlNamespaceDeclaration || (attr.Name.Space == "" && attr.Name.Local == xmlNamespaceDeclaration) {
			continue
		}
		name := attr.Name
		if name.Space != "" {
			namespace := namespaces[name.Space]
			if namespace == "" {
				return fmt.Errorf("%w: undeclared XML attribute prefix", errConfigSyntax)
			}
			name.Space = namespace
		}
		if attrs[name] {
			return fmt.Errorf("%w: duplicate XML attribute", errConfigSyntax)
		}
		attrs[name] = true
	}
	return nil
}

func validateXMLInstruction(instruction xml.ProcInst, offset int64) error {
	if strings.EqualFold(instruction.Target, xmlNamespacePrefix) && (offset != 0 || instruction.Target != xmlNamespacePrefix) {
		return fmt.Errorf("%w: misplaced XML declaration", errConfigSyntax)
	}
	return nil
}
