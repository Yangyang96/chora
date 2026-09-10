package domain

import "errors"

var (
	ErrInvalidID                  = errors.New("invalid domain ID")
	ErrInvalidArgument            = errors.New("invalid domain argument")
	ErrInvalidRunTransition       = errors.New("invalid run transition")
	ErrInvalidAttemptTransition   = errors.New("invalid attempt transition")
	ErrUnauthorizedReview         = errors.New("review decision is not device-owner authorized")
	ErrInvalidCandidateTransition = errors.New("invalid candidate transition")
)
