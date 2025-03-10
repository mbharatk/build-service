/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package github

import (
	"context"
	"fmt"
	"net/http"
	"time"

	ghinstallation "github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v45/github"
	"golang.org/x/oauth2"
)

// Allow mocking for tests
var NewGithubClientByApp func(appId int64, privateKeyPem []byte, owner string) (*GithubClient, error) = newGithubClientByApp
var NewGithubClient func(accessToken string) *GithubClient = newGithubClient

type GithubClient struct {
	ctx    context.Context
	client *github.Client
}

func newGithubClient(accessToken string) *GithubClient {
	gh := &GithubClient{}
	gh.ctx = context.Background()

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: accessToken},
	)
	tc := oauth2.NewClient(gh.ctx, ts)

	gh.client = github.NewClient(tc)

	return gh
}

func newGithubClientByApp(appId int64, privateKeyPem []byte, owner string) (*GithubClient, error) {
	itr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, appId, privateKeyPem) // 172616 (appstudio) 184730(Michkov)
	if err != nil {
		return nil, err
	}
	client := github.NewClient(&http.Client{Transport: itr})
	if err != nil {
		return nil, err
	}
	installations, _, err := client.Apps.ListInstallations(context.Background(), &github.ListOptions{})
	if err != nil {
		return nil, err
	}
	var installID int64
	for _, val := range installations {
		if val.GetAccount().GetLogin() == owner {
			installID = val.GetID()
		}
	}
	token, _, err := client.Apps.CreateInstallationToken(
		context.Background(),
		installID,
		&github.InstallationTokenOptions{})
	if err != nil {
		return nil, err
	}

	return NewGithubClient(token.GetToken()), nil
}

func (c *GithubClient) GetReference(owner, repository, branch string) (*github.Reference, error) {
	ref, _, err := c.client.Git.GetRef(c.ctx, owner, repository, "refs/heads/"+branch)
	return ref, err
}

func (c *GithubClient) GetOrCreateBranchReference(owner, repository, branch, baseBranch string) (*github.Reference, error) {
	ref, resp, err := c.client.Git.GetRef(c.ctx, owner, repository, "refs/heads/"+branch)
	if err == nil {
		return ref, nil
	} else if resp.StatusCode != 404 {
		return nil, err
	}

	baseBranchRef, err := c.GetReference(owner, repository, baseBranch)
	if err != nil {
		return nil, err
	}
	newBranchRef := &github.Reference{
		Ref:    github.String("refs/heads/" + branch),
		Object: &github.GitObject{SHA: baseBranchRef.Object.SHA},
	}
	ref, _, err = c.client.Git.CreateRef(c.ctx, owner, repository, newBranchRef)
	return ref, err
}

func (c *GithubClient) CreateTree(owner, repository string, baseRef *github.Reference, files []File) (tree *github.Tree, err error) {
	// Load each file into the tree.
	entries := []*github.TreeEntry{}
	for _, file := range files {
		entries = append(entries, &github.TreeEntry{Path: github.String(file.Name), Type: github.String("blob"), Content: github.String(string(file.Content)), Mode: github.String("100644")})
	}

	tree, _, err = c.client.Git.CreateTree(c.ctx, owner, repository, *baseRef.Object.SHA, entries)
	return tree, err
}

func (c *GithubClient) AddCommitToBranchReference(owner, repository, authorName, authorEmail, commitMessage string, files []File, ref *github.Reference) error {
	// Get the parent commit to attach the commit to.
	parent, _, err := c.client.Repositories.GetCommit(c.ctx, owner, repository, *ref.Object.SHA, nil)
	if err != nil {
		return err
	}
	// This is not always populated, but is needed.
	parent.Commit.SHA = parent.SHA

	tree, err := c.CreateTree(owner, repository, ref, files)
	if err != nil {
		return err
	}

	// Create the commit using the tree.
	date := time.Now()
	author := &github.CommitAuthor{Date: &date, Name: &authorName, Email: &authorEmail}
	commit := &github.Commit{Author: author, Message: &commitMessage, Tree: tree, Parents: []*github.Commit{parent.Commit}}
	newCommit, _, err := c.client.Git.CreateCommit(c.ctx, owner, repository, commit)
	if err != nil {
		return err
	}

	// Attach the created commit to the given branch.
	ref.Object.SHA = newCommit.SHA
	_, _, err = c.client.Git.UpdateRef(c.ctx, owner, repository, ref, false)
	return err
}

// CreatePullRequestWithinRepository create a new pull request into the same repository.
// Returns url to the created pull request.
func (c *GithubClient) CreatePullRequestWithinRepository(owner, repository, branchName, baseBranchName, prTitle, prText string) (string, error) {
	branch := fmt.Sprintf("%s:%s", owner, branchName)

	newPRData := &github.NewPullRequest{
		Title:               &prTitle,
		Head:                &branch,
		Base:                &baseBranchName,
		Body:                &prText,
		MaintainerCanModify: github.Bool(true),
	}

	pr, _, err := c.client.PullRequests.Create(c.ctx, owner, repository, newPRData)
	if err != nil {
		return "", err
	}

	return pr.GetHTMLURL(), nil
}

// GetWebhookByTargetUrl returns webhook by its target url or nil if such webhook doesn't exist.
func (c *GithubClient) GetWebhookByTargetUrl(owner, repository, webhookTargetUrl string) (*github.Hook, error) {
	// Suppose that the repository does not have more than 100 webhooks
	listOpts := &github.ListOptions{PerPage: 100}
	webhooks, _, err := c.client.Repositories.ListHooks(c.ctx, owner, repository, listOpts)
	if err != nil {
		return nil, err
	}

	for _, webhook := range webhooks {
		if webhook.Config["url"] == webhookTargetUrl {
			return webhook, nil
		}
	}
	// Webhook with the given URL not found
	return nil, nil
}

func (c *GithubClient) CreateWebhook(owner, repository string, webhook *github.Hook) (*github.Hook, error) {
	webhook, _, err := c.client.Repositories.CreateHook(c.ctx, owner, repository, webhook)
	return webhook, err
}

func (c *GithubClient) UpdateWebhook(owner, repository string, webhook *github.Hook) (*github.Hook, error) {
	webhook, _, err := c.client.Repositories.EditHook(c.ctx, owner, repository, *webhook.ID, webhook)
	return webhook, err
}
