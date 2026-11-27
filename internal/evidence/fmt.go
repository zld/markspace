package evidence

import "strconv"

func itoa(v int) string { return strconv.Itoa(v) }

// ftoa renders a float with fixed precision so reruns print identical text.
func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }
