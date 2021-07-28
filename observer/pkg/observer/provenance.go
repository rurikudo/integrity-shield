//
// Copyright 2020 IBM Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package observer

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"

	"github.com/ghodss/yaml"
	"github.com/in-toto/in-toto-golang/in_toto"
	k8smnfutil "github.com/sigstore/k8s-manifest-sigstore/pkg/util"
	log "github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func GetProvenanceFromVerifyResourceResult(res VerifyResult) ObservationResourceResult {
	var resourceLog ObservationResourceResult
	resourceLog.Kind = res.Resource.GroupVersionKind().Kind
	resourceLog.Namespace = res.Resource.GetNamespace()
	resourceLog.Name = res.Resource.GetName()
	resourceLog.Resource = res.Resource

	if len(res.Provenances) == 0 {
		log.Debug("Provenances is empty")
		return resourceLog
	}

	for _, pr := range res.Provenances {
		if pr.ArtifactType != k8smnfutil.ArtifactManifestImage {
			log.Debug("ArtifactType is not manifestImage:", pr.ArtifactType)
			continue
		}
		log.Debug("Provenances", pr)
		if len(pr.AttestationMaterials) != 0 {
			for _, am := range pr.AttestationMaterials {
				var mprovinfo ManifestProvenanceInfo
				commitID := getCommitID(am.Digest)
				url := convertToCommitDetailURL(am.URI, commitID)
				mprovinfo.Artifact = pr.Artifact
				mprovinfo.CommitID = commitID
				mprovinfo.GitApiURL = url
				mprovinfo.Hash = pr.Hash
				mprovinfo.GitRepo = am.URI
				resourceLog.ManifestProvenanceInfo = append(resourceLog.ManifestProvenanceInfo, mprovinfo)
			}
		}
	}
	return resourceLog
}

func setNewManifestProvenanceResult(prov ManifestProvenanceInfo) ManifestProvenanceResult {
	token := os.Getenv("GIT_TOKEN")
	data := accessGitRepo(prov.GitApiURL, token)
	cmtd := getCommitInfoFromDetail(data, prov.CommitID)
	mpres := ManifestProvenanceResult{
		GitRepo:    prov.GitRepo,
		GitApiURL:  prov.GitApiURL,
		CommitID:   prov.CommitID,
		CommitDate: cmtd.Date,
		Author:     cmtd.Author,
		Files:      cmtd.Files,
		Hash:       prov.Hash,
		Artifact:   prov.Artifact,
	}
	return mpres
}

func getCommitInfo(attestation string) []CommitData {
	res := []CommitData{}
	token := os.Getenv("GIT_TOKEN")
	var statement *in_toto.Statement
	err := json.Unmarshal([]byte(attestation), &statement)
	if err != nil {
		fmt.Println("Failed to unmarshal attestation; err: ", err.Error())
	}
	predicate := statement.Predicate
	materials, found := predicate.(map[string]interface{})["materials"]
	if !found {
		fmt.Println("Failed to get materials from predicate")
	}
	materialsArray, ok := materials.([]interface{})
	if !ok {
		fmt.Println("Failed to convert into materialsArray")
	}

	for _, m := range materialsArray {
		uri := m.(map[string]interface{})["uri"]
		digest := m.(map[string]interface{})["digest"]
		commit := digest.(map[string]interface{})["commit"]
		commitStr := commit.(string)
		url := convertToCommitDetailURL(uri.(string), commitStr)
		data := accessGitRepo(url, token)
		cmtd := getCommitInfoFromDetail(data, commitStr)
		res = append(res, cmtd)
	}
	return res
}

type CommitData struct {
	Commit string   `json:"commit"`
	Date   string   `json:"date"`
	Author string   `json:"author"`
	Files  []string `json:"files"`
}

type Parent struct {
	Commit string `json:"commit"`
	URL    string `json:"url"`
}

type Material struct {
	URI    string `json:"uri"`
	Digest struct {
		Commit   string `json:"commit"`
		Revision string `json:"revision"`
	} `json:"digest"`
}

type Materials []Material

func convertToCommitDetailURL(uri string, commit string) (url string) {
	// "https://github.com/user/sample-app.git"
	//  https://api.github.com/repos/user/sample-app/commits/xxxxx
	replaced := strings.Replace(uri, ".git", "/commits", 1)
	replaced1 := strings.Replace(replaced, "github.com", "api.github.com/repos", 1)
	url = replaced1 + "/" + commit
	return url
}

func convertToCommitHistoryURL(uri string, commit string) (url string) {
	// "https://github.com/user/sample-app.git"
	//  https://api.github.com/repos/user/sample-app/commits
	replaced := strings.Replace(uri, ".git", "/commits", 1)
	url = strings.Replace(replaced, "github.com", "api.github.com/repos", 1)
	return url
}

func accessGitRepo(url string, token string) []byte {
	var bearer = "Bearer " + token
	// Create a new request using http
	req, err := http.NewRequest("GET", url, nil)
	// add authorization header to the req
	req.Header.Add("Authorization", bearer)
	transCfg := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: transCfg}

	res, err := client.Get(url)
	if err != nil {
		log.Error("Error reported from GitHub API", err.Error())
	}
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		log.Error("Error: fail to read body: ", err)
	}
	return body
}

func getCommitInfoFromDetail(body []byte, cmtid string) CommitData {
	var data map[string]interface{}
	err := json.Unmarshal(body, &data)
	if err != nil {
		fmt.Println("Failed to unmarshal git data; err: ", err.Error())
	}
	var cmtdata CommitData
	// commit
	cmtdata.Commit = cmtid
	// author and date
	author := data["commit"].(map[string]interface{})["author"].(map[string]interface{})["email"]
	if author != nil {
		cmtdata.Author = author.(string)
	}
	date := data["commit"].(map[string]interface{})["author"].(map[string]interface{})["date"]
	cmtdata.Date = date.(string)
	// files
	files := data["files"].([]interface{})
	var fileNames []string
	for _, file := range files {
		name := file.(map[string]interface{})["filename"].(string)
		fileNames = append(fileNames, name)
	}
	cmtdata.Files = fileNames
	return cmtdata
}

func getParentsFromDetail(body []byte) []Parent {
	var data map[string]interface{}
	err := json.Unmarshal(body, &data)
	if err != nil {
		fmt.Println("Failed to unmarshal git data; err: ", err.Error())
	}
	parents := data["parents"].([]interface{})
	var result []Parent
	for _, parent := range parents {
		p := Parent{}
		url := parent.(map[string]interface{})["url"].(string)
		p.URL = url
		sha := parent.(map[string]interface{})["sha"].(string)
		p.Commit = sha
		result = append(result, p)
	}
	return result
}

func getCommitID(digest k8smnfutil.DigestSet) string {
	if val, ok := digest["commit"]; ok {
		return val
	}
	return ""
}

func getTargetResourceLog(res ObservationResourceResult, lastResults []FinalObservationResourceResult) (bool, FinalObservationResourceResult) {
	for _, lres := range lastResults {
		if lres.Kind == res.Kind && lres.Namespace == res.Namespace && lres.Name == res.Name {
			return true, lres
		}
	}
	return false, FinalObservationResourceResult{}
}

func getTargetProvenanceLog(new ManifestProvenanceInfo, lastProvs []ManifestProvenanceResult) (bool, ManifestProvenanceResult) {
	for _, prov := range lastProvs {
		if prov.GitRepo == new.GitRepo {
			return true, prov
		}
	}

	return false, ManifestProvenanceResult{}
}

func getManifestYaml(imageRef string, obj unstructured.Unstructured) (bool, []byte) {
	image, err := k8smnfutil.PullImage(imageRef)
	if err != nil {
		log.Error("failed to pull image to get manifest yaml", err.Error())
		return false, nil
	}
	concatYAMLbytes, err := k8smnfutil.GenerateConcatYAMLsFromImage(image)
	if err != nil {
		log.Error("failed to get contact yaml from image", err.Error())
		return false, nil
	}
	objBytes, err := yaml.Marshal(obj)
	if err != nil {
		log.Error("failed to marchal resource obj", err.Error())
		return false, nil
	}
	apiVersion := obj.GetAPIVersion()
	kind := obj.GetKind()
	name := obj.GetName()
	namespace := obj.GetNamespace()

	// extract candidate manifests that have an identical kind with object
	yamls := k8smnfutil.SplitConcatYAMLs(concatYAMLbytes)
	kindMatchedYAMLs := [][]byte{}
	for _, manifest := range yamls {
		var mnfObj *unstructured.Unstructured
		err := yaml.Unmarshal(manifest, &mnfObj)
		if err != nil {
			continue
		}
		mnfKind := mnfObj.GetKind()
		if kind == mnfKind {
			kindMatchedYAMLs = append(kindMatchedYAMLs, manifest)
		}
	}

	if len(kindMatchedYAMLs) == 0 {
		log.Error("no extract Kind Matched Manifests")
		return false, nil
	}
	candidateManifestBytes := k8smnfutil.ConcatenateYAMLs(kindMatchedYAMLs)
	if candidateManifestBytes == nil {
		log.Debug("candidateManifestBytes is nil")
		return false, nil
	}
	// manifest search based on gvk/name/namespace
	found, foundBytes := k8smnfutil.ManifestSearchByGVKNameNamespace(candidateManifestBytes, apiVersion, kind, name, namespace)
	if found {
		log.Debug("ManifestSearchByGVKNameNamespace", string(foundBytes))
		return found, foundBytes
	}
	// content-based manifest search
	found, foundBytes, _ = k8smnfutil.ManifestSearchByContent(candidateManifestBytes, objBytes, nil, nil)
	if found {
		log.Debug("ManifestSearchByContent", string(foundBytes))
		return found, foundBytes
	}
	return false, nil
	// found, foundManifest := k8smnfutil.FindManifestYAML(concatYAMLbytes, objBytes)
	// if found {
	// 	log.Debug("FindManifestYAML", string(foundManifest))
	// 	return found, foundManifest
	// }
	// return false, nil
}
