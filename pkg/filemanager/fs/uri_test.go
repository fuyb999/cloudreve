package fs

import "testing"

func TestSearchParametersExt(t *testing.T) {
	uri, err := NewUriFromString("cloudreve://my/?type=file&ext=JPG&ext=.png")
	if err != nil {
		t.Fatalf("failed to parse uri: %v", err)
	}

	params := uri.SearchParameters()
	if params == nil {
		t.Fatal("expected search parameters")
	}

	if len(params.Ext) != 2 || params.Ext[0] != "JPG" || params.Ext[1] != ".png" {
		t.Fatalf("unexpected ext values: %#v", params.Ext)
	}
}
