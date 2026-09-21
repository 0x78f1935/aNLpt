package config

import (
	"reflect"
	"testing"
)

// One folder with everything in it, and still a say per series, season and episode.
func TestTheDeepestWordWins(t *testing.T) {
	folder := Folder{Visibility: "members"}
	folder.SetAudience([]string{"Alfred J. Kwak"}, "public")
	folder.SetAudience([]string{"Alfred J. Kwak/Seizoen 2"}, "private")
	folder.SetAudience([]string{"Alfred J. Kwak/Seizoen 2/S02E03.mkv"}, "members")

	for path, want := range map[string]string{
		"Alfred J. Kwak/Seizoen 1/S01E01.mkv": "public",
		"Alfred J. Kwak/Seizoen 2/S02E01.mkv": "private",
		"Alfred J. Kwak/Seizoen 2/S02E03.mkv": "members",
		"Bassie en Adriaan/S01E01.mkv":        "", // nothing said: as the folder
		"Alfred J. Kwakkel/S01E01.mkv":        "", // a name that only starts the same
	} {
		if got := folder.AudienceOf(path); got != want {
			t.Errorf("%s is for %q, want %q", path, got, want)
		}
	}
}

// Somebody who sets a whole series means the whole series: what was said about a season
// in it goes. And saying "as above" takes the say away without touching the rest.
func TestSettingSomethingReplacesWhatWasSaidInsideIt(t *testing.T) {
	folder := Folder{}
	folder.SetAudience([]string{"Kwak/Seizoen 2", "Kwak/Seizoen 3/S03E01.mkv", "Bassie"}, "private")

	folder.SetAudience([]string{"Kwak"}, "public")

	want := []Rule{{Path: "Bassie", Visibility: "private"}, {Path: "Kwak", Visibility: "public"}}
	if !reflect.DeepEqual(folder.Rules, want) {
		t.Fatalf("rules are %+v", folder.Rules)
	}

	folder.SetAudience([]string{"Kwak", "Bassie"}, "")
	if len(folder.Rules) != 0 {
		t.Fatalf("taking the say away left %+v", folder.Rules)
	}
}

// A rule has two sides, and setting one leaves the other: a series given away that also
// has an English track is one rule that says both.
func TestWhoMayFetchItAndWhatItIsSpokenInAreSetApart(t *testing.T) {
	folder := Folder{Language: "nl"}
	folder.SetAudience([]string{"Kwak"}, "public")
	folder.SetLanguages([]string{"Kwak", "Bassie"}, "nl", []string{"en", "nl"})

	want := []Rule{
		{Path: "Bassie", Language: "nl", OtherLanguages: []string{"en"}},
		{Path: "Kwak", Visibility: "public", Language: "nl", OtherLanguages: []string{"en"}},
	}
	if !reflect.DeepEqual(folder.Rules, want) {
		t.Fatalf("rules are %+v", folder.Rules)
	}
	if language, others := folder.LanguagesOf("Kwak/Seizoen 1/a.mkv"); language != "nl" || !reflect.DeepEqual(others, []string{"en"}) {
		t.Fatalf("an episode of it is in %q and %v", language, others)
	}
	if language, _ := folder.LanguagesOf("Pipo/a.mkv"); language != "" {
		t.Fatalf("a series nothing was said about is in %q", language)
	}

	// Taking one side away leaves the other, and a rule that says nothing goes.
	folder.SetLanguages([]string{"Kwak", "Bassie"}, "", nil)
	if want := []Rule{{Path: "Kwak", Visibility: "public"}}; !reflect.DeepEqual(folder.Rules, want) {
		t.Fatalf("after taking the languages away: %+v", folder.Rules)
	}
	folder.SetAudience([]string{"Kwak"}, "")
	if len(folder.Rules) != 0 {
		t.Fatalf("after taking everything away: %+v", folder.Rules)
	}
}
