package runtime

import (
	"strings"
	"testing"
)

func feedAllChunks(chunks []string) (string, string) {
	p := newThinkStreamParser()
	var vis, th strings.Builder
	for _, ch := range chunks {
		v, t := p.Feed(ch)
		vis.WriteString(v)
		th.WriteString(t)
	}
	v, t := p.Flush()
	vis.WriteString(v)
	th.WriteString(t)
	return vis.String(), th.String()
}

func TestThinkStreamParserSplitTags(t *testing.T) {
	vis, th := feedAllChunks([]string{
		"<th",
		"ink>plan step 1; ",
		"step 2</th",
		"ink>Hello ",
		"world!",
	})
	if th != "plan step 1; step 2" {
		t.Fatalf("thinking = %q", th)
	}
	if vis != "Hello world!" {
		t.Fatalf("visible = %q", vis)
	}
}

func TestThinkStreamParserMultipleBlocksAndCaseInsensitive(t *testing.T) {
	vis, th := feedAllChunks([]string{
		"Intro <THINK>first",
		"</Think> mid <tHiNk>second</THINK> end",
	})
	if th != "firstsecond" {
		t.Fatalf("thinking = %q", th)
	}
	if vis != "Intro  mid  end" {
		t.Fatalf("visible = %q", vis)
	}
}

func TestThinkStreamParserPreservesPlainAngleBrackets(t *testing.T) {
	vis, th := feedAllChunks([]string{
		"a < b and x <t",
		"hat> y <",
	})
	if th != "" {
		t.Fatalf("expected empty thinking, got %q", th)
	}
	if vis != "a < b and x <that> y <" {
		t.Fatalf("visible = %q", vis)
	}
}

func TestThinkStreamParserFlushesUnclosedThinkAndNestedLessThan(t *testing.T) {
	vis, th := feedAllChunks([]string{
		"<t<think>1 < 2 is true</th",
		"ink>tail",
	})
	if vis != "<ttail" {
		t.Fatalf("visible = %q", vis)
	}
	if th != "1 < 2 is true" {
		t.Fatalf("thinking = %q", th)
	}

	vis2, th2 := feedAllChunks([]string{"<think>unclosed<"})
	if vis2 != "" || th2 != "unclosed<" {
		t.Fatalf("unclosed flush = (%q, %q)", vis2, th2)
	}
}
