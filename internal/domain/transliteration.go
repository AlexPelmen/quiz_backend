package domain

import "strings"

// TransliterateRussian заменяет строчные русские буквы латинской транслитерацией.
func TransliterateRussian(name string) string {
	letters := map[rune]string{
		'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
		'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
		'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
		'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
		'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	}
	var out strings.Builder
	for _, r := range name {
		if replacement, ok := letters[r]; ok {
			out.WriteString(replacement)
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
