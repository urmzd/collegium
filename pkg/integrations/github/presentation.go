package github

import (
	transport "github.com/urmzd/mandatum/pkg/connectors/github"
	presentation "github.com/urmzd/vultus/go/github"
)

// Review contract aliases keep presentation owned by vultus.
type ReviewReport = presentation.ReviewReport
type ReviewFinding = presentation.ReviewFinding
type ReviewEvidence = presentation.ReviewEvidence
type ReviewLink = presentation.ReviewLink

const ReviewFormat = presentation.ReviewFormat

var NewHTTPAPIAt = transport.NewHTTPAPIAt
