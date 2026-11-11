package main

import "strings"

var youtubeWords = []string{
	"youtube music", "ютуб музыку", "ютуб музыка", "ютуб музыке",
	"на youtube", "в youtube", "с youtube", "из youtube", "youtube",
	"на ютубе", "в ютубе", "с ютуба", "из ютуба", "по ютубу", "ютубе", "ютуба", "ютубу", "ютуб",
}

// mentionsYouTube reports a «ютуб»/«youtube» word anywhere in the phrase.
func mentionsYouTube(q string) bool {
	for _, word := range strings.Fields(q) {
		if strings.HasPrefix(word, "ютуб") || strings.HasPrefix(word, "youtube") {
			return true
		}
	}
	return false
}

// withoutYouTube removes the service name wherever it was said:
// «включи трек numb с ютуба» and «включи на ютубе numb» search for «numb».
func withoutYouTube(q string) string {
	words := " " + q + " "
	for _, phrase := range youtubeWords {
		words = strings.ReplaceAll(words, " "+phrase+" ", " ")
	}
	return strings.Join(strings.Fields(words), " ")
}
