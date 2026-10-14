package github

import (
	"github.com/urmzd/mandatum/pkg/address"
	transport "github.com/urmzd/mandatum/pkg/connectors/github"
	"strconv"
)

type API = transport.API
type ChangeRequest = transport.ChangeRequest
type Check = transport.Check
type CheckAPI = transport.CheckAPI
type CheckOutput = transport.CheckOutput
type CommitResult = transport.CommitResult
type Context = transport.Context
type ContextAPI = transport.ContextAPI
type ContextComment = transport.ContextComment
type ContextFile = transport.ContextFile
type ContextSubject = transport.ContextSubject
type Fake = transport.Fake
type FakeCheck = transport.FakeCheck
type FakeComment = transport.FakeComment
type FakeCommit = transport.FakeCommit
type FileAPI = transport.FileAPI
type FileContent = transport.FileContent
type FileEdit = transport.FileEdit
type Guide = transport.Guide
type GuideAPI = transport.GuideAPI
type HTTPAPI = transport.HTTPAPI
type MutationAPI = transport.MutationAPI
type NotFoundError = transport.NotFoundError
type ReactionCounts = transport.ReactionCounts
type Ref = transport.Ref
type ReplyAPI = transport.ReplyAPI
type Verifier = transport.Verifier
type WorkflowAPI = transport.WorkflowAPI
type WorkflowPage = transport.WorkflowPage
type WorkflowRun = transport.WorkflowRun

const Name = transport.Name
const HeaderEvent = transport.HeaderEvent
const HeaderDelivery = transport.HeaderDelivery
const MaxBodyChars = transport.MaxBodyChars

var NewFake = transport.NewFake
var NewHTTPAPI = transport.NewHTTPAPI

func refAddress(r Ref, kind string) address.Address {
	return address.Address{Connector: Name, Workspace: r.Owner, Path: []string{r.Repo, kind, strconv.Itoa(r.Number)}}
}
