package webhost

import (
	"reflect"
	"strings"
	"testing"

	"pi-go/internal/httpapi"
	"pi-go/internal/trajectory"
)

// TestTrajectoryMirrorsTheRowContract keeps the transport's copy of a row equal
// to the fold's own type.
//
// The two structs exist because httpapi must not learn the session format, and a
// mirror that lags behind is a field the window silently never receives. Nothing
// else would catch it: the fold's tests pass on its struct, the route's tests pass
// on the mirror, and the missing field only shows as an empty column in the page.
func TestTrajectoryMirrorsTheRowContract(t *testing.T) {
	source := describeFields(reflect.TypeOf(trajectory.Row{}))
	mirror := describeFields(reflect.TypeOf(httpapi.TrajectoryRow{}))
	if len(source) != len(mirror) {
		t.Fatalf("mirror has %d fields, the fold has %d:\nfold:   %s\nmirror: %s",
			len(mirror), len(source), strings.Join(source, " "), strings.Join(mirror, " "))
	}
	for i := range source {
		if source[i] != mirror[i] {
			t.Fatalf("field %d differs: fold %q, mirror %q", i, source[i], mirror[i])
		}
	}
}

// describeFields spells each field the way the wire sees it: its JSON name, its
// options, and the shape a decoder would have to accept.
func describeFields(structType reflect.Type) []string {
	out := make([]string, 0, structType.NumField())
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		shape := field.Type.Kind().String()
		if field.Type.Kind() == reflect.Pointer {
			shape = "*" + field.Type.Elem().String()
		}
		out = append(out, name+"/"+options+"/"+shape)
	}
	return out
}

// The fold answers for the branch in view. The faux kernel records a prompt, an
// answer and the rounds between them, so this checks the whole path a window
// takes: the manager's own entries, the fold, and the translation into the
// transport's row.
func TestTrajectoryFoldsTheConversationInView(t *testing.T) {
	host, manager := forkKernel(t)
	rows, err := host.Trajectory()
	if err != nil {
		t.Fatalf("trajectory: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no rows for a conversation with a prompt and an answer")
	}

	// The fold reports what was written, so the first row is the session naming
	// itself, not the prompt: the name was recorded before the first turn.
	if rows[0].Kind != string(trajectory.KindSessionInfo) || rows[0].Turn != 0 {
		t.Fatalf("first row = %+v, want the session's name before any turn", rows[0])
	}

	// The prompt that opened the first turn is recorded after that name, and it
	// belongs to turn 1 rather than to the rows before it.
	var prompt *httpapi.TrajectoryRow
	for i := range rows {
		if rows[i].Kind == string(trajectory.KindPrompt) {
			prompt = &rows[i]
			break
		}
	}
	if prompt == nil {
		t.Fatalf("rows = %+v, want the prompt the reader typed", rows)
	}
	if prompt.Turn != 1 {
		t.Fatalf("prompt turn = %d, want 1", prompt.Turn)
	}
	if prompt.Text != "hi" {
		t.Fatalf("prompt text = %q, want the recorded message", prompt.Text)
	}

	// Rows come back in file order. A round's calls share its seq, so the
	// numbering never decreases.
	for i := 1; i < len(rows); i++ {
		if rows[i].Seq < rows[i-1].Seq {
			t.Fatalf("row %d seq = %d, after %d", i, rows[i].Seq, rows[i-1].Seq)
		}
	}

	// The answer is a round of its own, after the prompt, and it carries the
	// text the reader saw.
	var answered bool
	for _, row := range rows {
		if row.Kind == string(trajectory.KindAssistant) && row.Text == "answer" {
			answered = true
		}
	}
	if !answered {
		t.Fatalf("rows = %+v, want the model's answer as a round", rows)
	}

	// The reply describes the session the manager is holding, so its entries are
	// the file's entries: the last row is the last thing recorded.
	if leaf := manager.LeafID(); leaf != nil {
		if last := rows[len(rows)-1]; last.EntryID != *leaf {
			t.Fatalf("last row entry = %q, want the leaf %q", last.EntryID, *leaf)
		}
	}
}
