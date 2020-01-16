/*
* [2013] - [2019] Avi Networks Incorporated
* All Rights Reserved.
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
*   http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package nodes

import (
	"fmt"

	avimodels "github.com/avinetworks/sdk/go/models"
	avicache "gitlab.eng.vmware.com/orion/akc/pkg/cache"
	"gitlab.eng.vmware.com/orion/container-lib/utils"
	extensionv1beta1 "k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Candidate for utils.
var shardSizeMap = map[string]uint32{
	"LARGE":  8,
	"MEDIUM": 4,
	"SMALL":  2,
}

// TODO: Move to utils
const tlsCert = "tls.crt"

func (o *AviObjectGraph) BuildL7VSGraph(vsName string, namespace string, ingName string, key string) {
	// We create pools and attach servers to them here. Pools are created with a priorty label of host/path
	utils.AviLog.Info.Printf("key: %s, msg: Building the L7 pools for namespace: %s, ingName: %s", key, namespace, ingName)
	ingObj, err := utils.GetInformers().IngressInformer.Lister().Ingresses(namespace).Get(ingName)
	pgName := vsName + utils.L7_PG_PREFIX
	pgNode := o.GetPoolGroupByName(pgName)
	vsNode := o.GetAviVS()
	if len(vsNode) != 1 {
		utils.AviLog.Warning.Printf("key: %s, msg: more than one vs in model.", key)
		return
	}
	if err != nil {
		// A case, where we detected in Layer 2 that the ingress has been deleted.
		if errors.IsNotFound(err) {
			utils.AviLog.Info.Printf("key: %s, msg: ingress not found:  %s", key, ingName)

			// Fetch the ingress pools that are present in the model and delete them.
			poolNodes := o.GetAviPoolNodesByIngress(utils.ADMIN_NS, ingName)
			utils.AviLog.Info.Printf("key: %s, msg: Pool Nodes to delete for ingress:  %s", key, utils.Stringify(poolNodes))

			for _, pool := range poolNodes {
				o.RemovePoolNodeRefs(pool.Name)
			}
		}
	} else {
		// First check if there are pools related to this ingress present in the model already
		poolNodes := o.GetAviPoolNodesByIngress(utils.ADMIN_NS, ingName)
		utils.AviLog.Info.Printf("key: %s, msg: found pools in the model: %s", key, utils.Stringify(poolNodes))
		for _, pool := range poolNodes {
			o.RemovePoolNodeRefs(pool.Name)
		}
		// PGs are in 'admin' namespace right now.
		if pgNode != nil {
			ingressConfig := parseHostPathForIngress(ingName, ingObj.Spec, key)
			utils.AviLog.Info.Printf("key: %s, msg: hostpathsvc list: %s", key, utils.Stringify(ingressConfig))
			// Processsing insecure ingress
			for host, val := range ingressConfig.IngressHostMap {
				for _, obj := range val {
					var priorityLabel string
					if obj.Path != "" {
						priorityLabel = host + obj.Path
					} else {
						priorityLabel = host
					}
					poolNode := &AviPoolNode{Name: priorityLabel + "--" + namespace + "--" + ingName, IngressName: ingName, Tenant: utils.ADMIN_NS, PriorityLabel: priorityLabel, Port: obj.Port, ServiceMetadata: avicache.ServiceMetadataObj{IngressName: ingName, Namespace: namespace}}
					if servers := PopulateServers(poolNode, namespace, obj.ServiceName, key); servers != nil {
						poolNode.Servers = servers
					}

					utils.AviLog.Info.Printf("key: %s, msg: the pools before append are: %v", key, utils.Stringify(vsNode[0].PoolRefs))
					vsNode[0].PoolRefs = append(vsNode[0].PoolRefs, poolNode)

				}
			}
			// Processing the TLS nodes
			for _, tlssetting := range ingressConfig.TlsCollection {
				// For each host, create a SNI node with the secret giving us the key and cert.
				// construct a SNI VS node per tls setting which corresponds to one secret
				sniNode := &AviVsNode{Name: ingName + "--" + tlssetting.SecretName, VHParentName: vsNode[0].Name, Tenant: utils.ADMIN_NS, IsSNIChild: true}
				certsBuilt := o.BuildTlsCertNode(sniNode, namespace, tlssetting.SecretName, key)
				if certsBuilt {
					o.BuildPolicyPGPoolsForSNI(sniNode, namespace, ingName, tlssetting, tlssetting.SecretName, key)
					vsNode[0].SniNodes = append(vsNode[0].SniNodes, sniNode)
				}
			}

		}
	}
	utils.AviLog.Info.Printf("key: %s, msg: the pool nodes are: %v", key, utils.Stringify(vsNode[0].PoolRefs))
	// Reset the PG Node members and rebuild them
	pgNode.Members = nil
	for _, poolNode := range vsNode[0].PoolRefs {
		pool_ref := fmt.Sprintf("/api/pool?name=%s", poolNode.Name)
		pgNode.Members = append(pgNode.Members, &avimodels.PoolGroupMember{PoolRef: &pool_ref, PriorityLabel: &poolNode.PriorityLabel})
	}
}

func parseHostPathForIngress(ingName string, ingSpec extensionv1beta1.IngressSpec, key string) IngressConfig {
	// Figure out the service names that are part of this ingress
	var hostPathMapSvcList []IngressHostPathSvc

	ingressConfig := IngressConfig{}
	hostMap := make(IngressHostMap)
	for _, rule := range ingSpec.Rules {
		var hostName string
		if rule.Host == "" {
			// The Host field is empty. Generate a hostName using the sub-domain info from configmap
			hostName = ingName // (TODO): Add sub-domain
		} else {
			hostName = rule.Host
		}
		for _, path := range rule.IngressRuleValue.HTTP.Paths {
			hostPathMapSvc := IngressHostPathSvc{}
			//hostPathMapSvc.Host = hostName
			hostPathMapSvc.Path = path.Path
			hostPathMapSvc.ServiceName = path.Backend.ServiceName
			hostPathMapSvc.Port = path.Backend.ServicePort.IntVal
			if hostPathMapSvc.Port == 0 {
				// Default to port 80 if not set in the ingress object
				hostPathMapSvc.Port = 80
			}
			hostPathMapSvcList = append(hostPathMapSvcList, hostPathMapSvc)
		}
		hostMap[hostName] = hostPathMapSvcList
	}
	tlsHostSvcMap := make(IngressHostMap)
	var tlsConfigs []TlsSettings
	for _, tlsSettings := range ingSpec.TLS {
		tls := TlsSettings{}
		tls.SecretName = tlsSettings.SecretName
		for _, host := range tlsSettings.Hosts {
			hostSvcMap, ok := hostMap[host]
			if ok {
				tlsHostSvcMap[host] = hostSvcMap
				delete(hostMap, host)
			}
		}
		tls.Hosts = tlsHostSvcMap
		tlsConfigs = append(tlsConfigs, tls)
	}
	ingressConfig.TlsCollection = tlsConfigs
	ingressConfig.IngressHostMap = hostMap
	utils.AviLog.Info.Printf("key: %s, msg: host path config from ingress:  %v", key, ingressConfig)
	return ingressConfig
}

func (o *AviObjectGraph) ConstructAviL7VsNode(vsName string, key string) *AviVsNode {
	var avi_vs_meta *AviVsNode
	// This is a shared VS - always created in the admin namespace for now.
	avi_vs_meta = &AviVsNode{Name: vsName, Tenant: utils.ADMIN_NS,
		EastWest: false, SharedVS: true}
	// Hard coded ports for the shared VS
	var portProtocols []AviPortHostProtocol
	httpPort := AviPortHostProtocol{Port: 80, Protocol: utils.HTTP}
	httpsPort := AviPortHostProtocol{Port: 443, Protocol: utils.HTTP, EnableSSL: true}
	portProtocols = append(portProtocols, httpPort)
	portProtocols = append(portProtocols, httpsPort)
	avi_vs_meta.PortProto = portProtocols
	// Default case.
	avi_vs_meta.ApplicationProfile = utils.DEFAULT_L7_SECURE_APP_PROFILE
	avi_vs_meta.NetworkProfile = utils.DEFAULT_TCP_NW_PROFILE
	avi_vs_meta.SNIParent = true
	o.AddModelNode(avi_vs_meta)
	o.ConstructShardVsPGNode(vsName, key, avi_vs_meta)
	o.ConstructHTTPDataScript(vsName, key, avi_vs_meta)
	return avi_vs_meta
}

func (o *AviObjectGraph) ConstructShardVsPGNode(vsName string, key string, vsNode *AviVsNode) *AviPoolGroupNode {
	pgName := vsName + utils.L7_PG_PREFIX
	pgNode := &AviPoolGroupNode{Name: pgName, Tenant: utils.ADMIN_NS, ImplicitPriorityLabel: true}
	vsNode.PoolGroupRefs = append(vsNode.PoolGroupRefs, pgNode)
	o.AddModelNode(pgNode)
	return pgNode
}

func (o *AviObjectGraph) ConstructHTTPDataScript(vsName string, key string, vsNode *AviVsNode) *AviHTTPDataScriptNode {
	scriptStr := utils.HTTP_DS_SCRIPT
	evt := utils.VS_DATASCRIPT_EVT_HTTP_REQ
	var poolGroupRefs []string
	pgName := vsName + utils.L7_PG_PREFIX
	poolGroupRefs = append(poolGroupRefs, pgName)
	dsName := vsName + "-http-datascript"
	script := &DataScript{Script: scriptStr, Evt: evt}
	dsScriptNode := &AviHTTPDataScriptNode{Name: dsName, Tenant: utils.ADMIN_NS, DataScript: script, PoolGroupRefs: poolGroupRefs}
	vsNode.HTTPDSrefs = append(vsNode.HTTPDSrefs, dsScriptNode)
	o.AddModelNode(dsScriptNode)
	return dsScriptNode
}

func (o *AviObjectGraph) BuildTlsCertNode(tlsNode *AviVsNode, namespace string, secretName string, key string) bool {
	mClient := utils.GetInformers().ClientSet
	secretObj, err := mClient.CoreV1().Secrets(namespace).Get(secretName, metav1.GetOptions{})
	if err != nil || secretObj == nil {
		// This secret has been deleted.
		utils.AviLog.Info.Printf("key: %s, msg: secret: %s has been deleted, err: %s", key, secretName, err)
		return false
	}
	certNode := &AviTLSKeyCertNode{Name: namespace + "-" + secretName, Tenant: utils.ADMIN_NS}
	keycertMap := secretObj.Data
	cert, ok := keycertMap[tlsCert]
	if ok {
		certNode.Cert = cert
	} else {
		utils.AviLog.Info.Printf("key: %s, msg: certificate not found for secret: %s", key, secretObj.Name)
		return false
	}
	tlsKey, keyfound := keycertMap[utils.K8S_TLS_SECRET_KEY]
	if keyfound {
		certNode.Key = tlsKey
	} else {
		utils.AviLog.Info.Printf("key: %s, msg: key not found for secret: %s", key, secretObj.Name)
		return false
	}
	utils.AviLog.Info.Printf("key: %s, msg: Added the scret object to tlsnode: %s", key, secretObj.Name)

	tlsNode.SSLKeyCertRefs = append(tlsNode.SSLKeyCertRefs, certNode)
	return true
}

func (o *AviObjectGraph) BuildPolicyPGPoolsForSNI(tlsNode *AviVsNode, namespace string, ingName string, hostpath TlsSettings, secretName string, key string) {
	var httpPolicySet []AviHostPathPortPoolPG
	for host, paths := range hostpath.Hosts {
		var hosts []string
		hosts = append(hosts, host)
		httpPGPath := AviHostPathPortPoolPG{Host: hosts}
		tlsNode.VHDomainNames = hosts
		for _, path := range paths {
			httpPGPath.Path = append(httpPGPath.Path, path.Path)
			httpPGPath.MatchCriteria = "EQUALS"
			pgName := namespace + "--" + ingName + "--" + host + "--" + path.Path
			pgNode := &AviPoolGroupNode{Name: pgName, Tenant: utils.ADMIN_NS}
			httpPGPath.PoolGroup = pgNode.Name
			httpPGPath.Host = hosts
			httpPolicySet = append(httpPolicySet, httpPGPath)

			tlsNode.PoolGroupRefs = append(tlsNode.PoolGroupRefs, pgNode)
			poolNode := &AviPoolNode{Name: namespace + "--" + ingName + "--" + host + "--" + path.Path, Tenant: utils.ADMIN_NS}

			if servers := PopulateServers(poolNode, namespace, path.ServiceName, key); servers != nil {
				poolNode.Servers = servers
			}
			pool_ref := fmt.Sprintf("/api/pool?name=%s", poolNode.Name)
			pgNode.Members = append(pgNode.Members, &avimodels.PoolGroupMember{PoolRef: &pool_ref})

			tlsNode.PoolRefs = append(tlsNode.PoolRefs, poolNode)

		}
	}
	httppolname := ingName + "--" + namespace + "--" + secretName
	policyNode := &AviHttpPolicySetNode{Name: httppolname, HppMap: httpPolicySet, Tenant: utils.ADMIN_NS}
	tlsNode.HttpPolicyRefs = append(tlsNode.HttpPolicyRefs, policyNode)
	utils.AviLog.Info.Printf("key: %s, msg: added pools and poolgroups to tlsNode: %s", key, utils.Stringify(tlsNode))

}