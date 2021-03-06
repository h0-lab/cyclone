/*
Copyright 2017 caicloud authors. All rights reserved.

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

package gitlab

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"

	"github.com/caicloud/nirvana/log"
	gitlabv3 "github.com/xanzy/go-gitlab"

	"github.com/caicloud/cyclone/pkg/api"
	"github.com/caicloud/cyclone/pkg/scm"
	"github.com/caicloud/cyclone/pkg/scm/provider"
	"github.com/caicloud/cyclone/pkg/util/http/errors"
)

// GitlabV3 represents the SCM provider of GitlabV3 with API V3.
type GitlabV3 struct {
	scmCfg *api.SCMConfig
	client *gitlabv3.Client
}

// GetToken gets the token by the username and password of SCM config.
func (g *GitlabV3) GetToken() (string, error) {
	return getOauthToken(g.scmCfg)
}

// CheckToken checks whether the token has the authority of repo by trying ListRepos with the token.
func (g *GitlabV3) CheckToken() bool {
	if _, err := g.listReposInner(false); err != nil {
		return false
	}
	return true
}

// ListRepos lists the repos by the SCM config.
func (g *GitlabV3) ListRepos() ([]api.Repository, error) {
	return g.listReposInner(true)
}

// listReposInner lists the projects by the SCM config,
// list all projects while the parameter 'listAll' is true,
// otherwise, list projects by default 'ListPerPageOpt' number.
func (g *GitlabV3) listReposInner(listAll bool) ([]api.Repository, error) {
	opt := &gitlabv3.ListProjectsOptions{
		ListOptions: gitlabv3.ListOptions{
			PerPage: provider.ListPerPageOpt,
		},
	}

	// Get all pages of results.
	var allProjects []*gitlabv3.Project
	for {
		projects, resp, err := g.client.Projects.ListProjects(opt)
		if err != nil {
			return nil, err
		}

		allProjects = append(allProjects, projects...)
		if resp.NextPage == 0 || !listAll {
			break
		}
		opt.ListOptions.Page = resp.NextPage
	}

	repos := make([]api.Repository, len(allProjects))
	for i, repo := range allProjects {
		repos[i].Name = repo.PathWithNamespace
		repos[i].URL = repo.HTTPURLToRepo
	}

	return repos, nil
}

// ListBranches lists the branches for specified repo.
func (g *GitlabV3) ListBranches(repo string) ([]string, error) {
	branches, _, err := g.client.Branches.ListBranches(repo)
	if err != nil {
		log.Errorf("Fail to list branches for %s", repo)
		return nil, err
	}

	branchNames := make([]string, len(branches))
	for i, branch := range branches {
		branchNames[i] = branch.Name
	}

	return branchNames, nil
}

// ListTags lists the tags for specified repo.
func (g *GitlabV3) ListTags(repo string) ([]string, error) {
	tags, _, err := g.client.Tags.ListTags(repo)
	if err != nil {
		log.Errorf("Fail to list tags for %s", repo)
		return nil, err
	}

	tagNames := make([]string, len(tags))
	for i, tag := range tags {
		tagNames[i] = tag.Name
	}

	return tagNames, nil
}

// ListDockerfiles lists the Dockerfiles for specified repo.
func (g *GitlabV3) ListDockerfiles(repo string) ([]string, error) {
	// List Dockerfiles in a project with gitlab v3 api is very inefficient.
	// There is not a proper api can be used to do this with GitLab v3.
	//
	// FYI:
	// https://stackoverflow.com/questions/25127695/search-filenames-with-gitlab-api
	return nil, errors.ErrorNotImplemented.Error("list gitlab v3 dockerfiles")
}

// CreateWebHook creates webhook for specified repo.
func (g *GitlabV3) CreateWebHook(repoURL string, webHook *scm.WebHook) error {
	if webHook == nil || len(webHook.Url) == 0 || len(webHook.Events) == 0 {
		return fmt.Errorf("The webhook %v is not correct", webHook)
	}

	enableState, disableState := true, false
	// Push event is enable for Gitlab webhook in default, so need to remove this default option.
	hook := gitlabv3.AddProjectHookOptions{
		PushEvents: &disableState,
	}

	for _, e := range webHook.Events {
		switch e {
		case scm.PullRequestEventType:
			hook.MergeRequestsEvents = &enableState
		case scm.PullRequestCommentEventType:
			hook.NoteEvents = &enableState
		case scm.PushEventType:
			hook.PushEvents = &enableState
		case scm.TagReleaseEventType:
			hook.TagPushEvents = &enableState
		default:
			log.Errorf("The event type %s is not supported, will be ignored", e)
			return nil
		}
	}
	hook.URL = &webHook.Url

	onwer, name := provider.ParseRepoURL(repoURL)
	_, _, err := g.client.Projects.AddProjectHook(onwer+"/"+name, &hook)
	log.Error(err)
	return err
}

// DeleteWebHook deletes webhook from specified repo.
func (g *GitlabV3) DeleteWebHook(repoURL string, webHookUrl string) error {
	owner, name := provider.ParseRepoURL(repoURL)
	hooks, _, err := g.client.Projects.ListProjectHooks(owner+"/"+name, nil)
	if err != nil {
		return err
	}

	for _, hook := range hooks {
		if strings.HasPrefix(hook.URL, webHookUrl) {
			_, err = g.client.Projects.DeleteProjectHook(owner+"/"+name, hook.ID)
			return nil
		}
	}

	return nil
}

// NewTagFromLatest generate a new tag
func (g *GitlabV3) NewTagFromLatest(tagName, description, commitID, url string) error {
	owner, name := provider.ParseRepoURL(url)
	tag := &gitlabv3.CreateTagOptions{
		TagName: &tagName,
		Ref:     &commitID,
		Message: &description,
	}

	_, _, err := g.client.Tags.CreateTag(owner+"/"+name, tag)
	log.Error(err)
	return err
}

func (g *GitlabV3) GetTemplateType(repo string) (string, error) {
	languages, err := getLanguages(g.scmCfg, v3APIVersion, repo)
	if err != nil {
		log.Error("list language failed:%v", err)
		return "", err
	}
	language := getTopLanguage(languages)

	switch language {
	case api.JavaRepoType, api.JavaScriptRepoType:
		files, err := getContents(g.scmCfg, v3APIVersion, repo)
		if err != nil {
			log.Error("get contents failed:%v", err)
			return language, nil
		}

		for _, f := range files {
			if language == api.JavaRepoType && strings.Contains(f.Name, "pom.xml") {
				return api.MavenRepoType, nil
			}
			if language == api.JavaRepoType && strings.Contains(f.Name, "build.gradle") {
				return api.GradleRepoType, nil
			}
			if language == api.JavaScriptRepoType && strings.Contains(f.Name, "package.json") {
				return api.NodeRepoType, nil
			}
		}

	}

	return language, nil
}

// CreateStatus generate a new status for repository.
func (g *GitlabV3) CreateStatus(recordStatus api.Status, targetURL, repoURL, commitSha string) error {
	state, description := transStatus(recordStatus)

	owner, project := provider.ParseRepoURL(repoURL)
	context := "continuous-integration/cyclone"
	status := &gitlabv3.SetCommitStatusOptions{
		State:       gitlabv3.BuildState(state),
		Description: &description,
		TargetURL:   &targetURL,
		Context:     &context,
	}
	_, _, err := g.client.Commits.SetCommitStatus(owner+"/"+project, commitSha, status)
	log.Error(err)
	return nil
}

func (g *GitlabV3) GetPullRequestSHA(repoURL string, number int) (string, error) {
	owner, name := provider.ParseRepoURL(repoURL)
	path := fmt.Sprintf("%s/api/%s/projects/%s/merge_requests?iid=%d",
		strings.TrimSuffix(g.scmCfg.Server, "/"), v3APIVersion, url.QueryEscape(owner+"/"+name), number)
	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}

	if len(g.scmCfg.Username) == 0 {
		req.Header.Set("PRIVATE-TOKEN", g.scmCfg.Token)
	} else {
		req.Header.Set("Authorization", "Bearer "+g.scmCfg.Token)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Errorf("Fail to get project merge request as %s", err.Error())
		return "", err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("Fail to get project merge request as %s", err.Error())
		return "", err
	}

	if resp.StatusCode/100 == 2 {
		mr := []mergeRequestResponse{}
		err := json.Unmarshal(body, &mr)
		if err != nil {
			return "", err
		}
		if len(mr) > 0 {
			return mr[0].SHA, nil
		}
		return "", fmt.Errorf("Merge request %d not found ", number)
	}

	err = fmt.Errorf("Fail to get merge request %d as %s ", number, body)
	return "", err
}

// mergeRequestResponse represents the response of Gitlab merge request API.
type mergeRequestResponse struct {
	ID           int    `json:"id"`
	IID          int    `json:"iid"`
	TargetBranch string `json:"target_branch"`
	SHA          string `json:"sha"`
}

func (g *GitlabV3) GetMergeRequestTargetBranch(repoURL string, number int) (string, error) {
	owner, name := provider.ParseRepoURL(repoURL)
	mr, _, err := g.client.MergeRequests.GetMergeRequest(owner+"/"+name, number)
	if err != nil {
		return "", err
	}

	return mr.TargetBranch, nil
}

func (g *GitlabV3) RetrieveRepoInfo(url string) (*api.RepoInfo, error) {
	return nil, errors.ErrorNotImplemented.Error("retrieve GitLab repo info")
}
