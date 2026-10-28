package setup

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// object is a JSON object that keeps its key order, so rewriting an
// operator's config file changes only what Roar meant to change.
type object []member

type member struct {
	key   string
	value json.RawMessage
}

func (o object) get(key string) (json.RawMessage, bool) {
	for _, m := range o {
		if m.key == key {
			return m.value, true
		}
	}
	return nil, false
}

// set replaces key in place, or appends it.
func (o *object) set(key string, v json.RawMessage) {
	for i, m := range *o {
		if m.key == key {
			(*o)[i].value = v
			return
		}
	}
	*o = append(*o, member{key, v})
}

func (o *object) UnmarshalJSON(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return fmt.Errorf("not a JSON object")
	}
	*o = nil
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return err
		}
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return err
		}
		*o = append(*o, member{t.(string), v})
	}
	return nil
}

func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(m.key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
