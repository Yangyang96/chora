package localweb

import "errors"

// Comment is canonical. Note is accepted only for clients predating the rename.
type reviewCommentInput struct {
	Comment    *string `json:"comment"`
	LegacyNote *string `json:"note,omitempty"`
}

func (input reviewCommentInput) value() (string, error) {
	if input.Comment != nil {
		if input.LegacyNote != nil && *input.LegacyNote != *input.Comment {
			return "", errors.New("comment and legacy note must agree")
		}
		return *input.Comment, nil
	}
	if input.LegacyNote != nil {
		return *input.LegacyNote, nil
	}
	return "", nil
}
