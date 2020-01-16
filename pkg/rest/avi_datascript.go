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

package rest

import (
	"errors"
	"strings"

	avimodels "github.com/avinetworks/sdk/go/models"
	"github.com/davecgh/go-spew/spew"
	avicache "gitlab.eng.vmware.com/orion/akc/pkg/cache"
	"gitlab.eng.vmware.com/orion/akc/pkg/nodes"
	"gitlab.eng.vmware.com/orion/container-lib/utils"
)

func (rest *RestOperations) AviDSBuild(ds_meta *nodes.AviHTTPDataScriptNode, cache_obj *avicache.AviDSCache, key string) *utils.RestOp {
	var datascriptlist []*avimodels.VSDataScript
	var poolgroupref []string
	if len(ds_meta.PoolGroupRefs) > 0 {
		// Replace the PoolGroup Ref in the DS.
		ds_meta.Script = strings.Replace(ds_meta.Script, "POOLGROUP", ds_meta.PoolGroupRefs[0], 1)
		pg_ref := "/api/poolgroup/?name=" + ds_meta.PoolGroupRefs[0]
		poolgroupref = append(poolgroupref, pg_ref)
	}
	datascript := avimodels.VSDataScript{Evt: &ds_meta.Evt, Script: &ds_meta.Script}
	datascriptlist = append(datascriptlist, &datascript)
	tenant_ref := "/api/tenant/?name=" + ds_meta.Tenant
	cr := utils.OSHIFT_K8S_CLOUD_CONNECTOR
	vsdatascriptset := avimodels.VSDataScriptSet{CreatedBy: &cr, Datascript: datascriptlist, Name: &ds_meta.Name, TenantRef: &tenant_ref, PoolGroupRefs: poolgroupref}

	var path string
	var rest_op utils.RestOp
	macro := utils.AviRestObjMacro{ModelName: "VSDataScriptSet", Data: vsdatascriptset}
	if cache_obj != nil {
		path = "/api/vsdatascriptset/" + cache_obj.Uuid
		rest_op = utils.RestOp{Path: path, Method: utils.RestPut, Obj: vsdatascriptset,
			Tenant: ds_meta.Tenant, Model: "VSDataScriptSet", Version: utils.CtrlVersion}

	} else {
		path = "/api/macro"
		rest_op = utils.RestOp{Path: path, Method: utils.RestPost, Obj: macro,
			Tenant: ds_meta.Tenant, Model: "VSDataScriptSet", Version: utils.CtrlVersion}
	}

	utils.AviLog.Info.Print(spew.Sprintf("key: %s, msg: ds Restop %v DatascriptData %v\n", key,
		utils.Stringify(rest_op), *ds_meta))
	return &rest_op
}

func (rest *RestOperations) AviDSDel(uuid string, tenant string, key string) *utils.RestOp {
	path := "/api/vsdatascriptset/" + uuid
	rest_op := utils.RestOp{Path: path, Method: "DELETE",
		Tenant: tenant, Model: "VSDataScriptSet", Version: utils.CtrlVersion}
	utils.AviLog.Info.Print(spew.Sprintf("key: %s, msg: DS DELETE Restop %v \n", key,
		utils.Stringify(rest_op)))
	return &rest_op
}

func (rest *RestOperations) AviDSCacheAdd(rest_op *utils.RestOp, vsKey avicache.NamespaceName, key string) error {
	if (rest_op.Err != nil) || (rest_op.Response == nil) {
		utils.AviLog.Warning.Printf("key: %s, rest_op has err or no reponse for datascriptset err: %s, response: %s", key, rest_op.Err, rest_op.Response)
		return errors.New("Errored rest_op")
	}

	resp_elems, ok := RestRespArrToObjByType(rest_op, "vsdatascriptset", key)
	utils.AviLog.Warning.Printf("The datascriptset object response %v", rest_op.Response)
	if ok != nil || resp_elems == nil {
		utils.AviLog.Warning.Printf("key: %s, msg: unable to find datascriptset obj in resp %v", key, rest_op.Response)
		return errors.New("datascriptset not found")
	}

	for _, resp := range resp_elems {
		name, ok := resp["name"].(string)
		if !ok {
			utils.AviLog.Warning.Printf("key: %s, msg: DS Name not present in response %v", key, resp)
			continue
		}

		uuid, ok := resp["uuid"].(string)
		if !ok {
			utils.AviLog.Warning.Printf("key: %s, msg: DS Uuid not present in response %v", key, resp)
			continue
		}
		// Datascript should not have a checksum
		//cksum := resp["cloud_config_cksum"].(string)

		ds_cache_obj := avicache.AviDSCache{Name: name, Tenant: rest_op.Tenant,
			Uuid: uuid}

		k := avicache.NamespaceName{Namespace: rest_op.Tenant, Name: name}
		rest.cache.DSCache.AviCacheAdd(k, &ds_cache_obj)
		// Update the VS object
		vs_cache, ok := rest.cache.VsCache.AviCacheGet(vsKey)
		if ok {
			vs_cache_obj, found := vs_cache.(*avicache.AviVsCache)
			if found {
				if vs_cache_obj.DSKeyCollection == nil {
					vs_cache_obj.DSKeyCollection = []avicache.NamespaceName{k}
				} else {
					if !utils.HasElem(vs_cache_obj.DSKeyCollection, k) {
						utils.AviLog.Info.Printf("key: %s, msg: before adding datascriptset collection %v and key :%v", key, vs_cache_obj.PoolKeyCollection, k)
						vs_cache_obj.DSKeyCollection = append(vs_cache_obj.DSKeyCollection, k)
					}
				}
				utils.AviLog.Info.Printf("key: %s, msg: modified the VS cache object for Datascriptset Collection. The cache now is :%v", key, utils.Stringify(vs_cache_obj))
			}
		} else {
			vs_cache_obj := avicache.AviVsCache{Name: vsKey.Name, Tenant: vsKey.Namespace,
				DSKeyCollection: []avicache.NamespaceName{k}}
			rest.cache.VsCache.AviCacheAdd(vsKey, &vs_cache_obj)
			utils.AviLog.Info.Print(spew.Sprintf("key: %s, msg: added VS cache key during datascriptset update %v val %v\n", key, vsKey,
				vs_cache_obj))
		}
		utils.AviLog.Info.Print(spew.Sprintf("key: %s, msg: added Datascriptset cache k %v val %v\n", key, k,
			ds_cache_obj))
	}

	return nil
}

func (rest *RestOperations) AviDSCacheDel(rest_op *utils.RestOp, vsKey avicache.NamespaceName, key string) error {
	dsKey := avicache.NamespaceName{Namespace: rest_op.Tenant, Name: rest_op.ObjName}
	rest.cache.DSCache.AviCacheDelete(key)
	vs_cache, ok := rest.cache.VsCache.AviCacheGet(vsKey)
	if ok {
		vs_cache_obj, found := vs_cache.(*avicache.AviVsCache)
		if found {
			vs_cache_obj.DSKeyCollection = Remove(vs_cache_obj.DSKeyCollection, dsKey)
		}
	}

	return nil
}