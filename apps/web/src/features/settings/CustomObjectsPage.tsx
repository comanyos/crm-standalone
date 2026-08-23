import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PageHeader } from "../../components/shell/PageHeader";
import { Button } from "../../components/ui/Button";
import { Dialog } from "../../components/ui/Dialog";
import { Input } from "../../components/ui/Input";
import { Table } from "../../components/ui/Table";
import { EmptyState } from "../../components/ui/StatePanel";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { useAuth } from "../../auth/AuthProvider";
import { useCrmClients } from "../../lib/useCrmClients";
import { mutationErrorMessage, renderQueryError } from "../../lib/connectErrors";
import { useToast } from "../../components/ui/Toast";

export default function CustomObjectsPage() {
  const { crm } = useCrmClients(); const { hasScope } = useAuth(); const toast = useToast(); const queryClient = useQueryClient();
  const [open,setOpen]=useState(false); const [apiName,setApiName]=useState(""); const [label,setLabel]=useState(""); const [plural,setPlural]=useState("");
  const canRead=hasScope("crm:custom_object:read"); const canWrite=hasScope("crm:custom_object:write");
  const query=useQuery({queryKey:["crm.listCustomObjectDefinitions"],queryFn:()=>crm.listCustomObjectDefinitions({includeArchived:true}),enabled:canRead}); const objects=query.data?.definitions??[];
  const refresh=()=>queryClient.invalidateQueries({queryKey:["crm.listCustomObjectDefinitions"]}); const close=()=>{setOpen(false);setApiName("");setLabel("");setPlural("");};
  const create=useMutation({mutationFn:()=>crm.createCustomObjectDefinition({definition:{apiName,label,pluralLabel:plural}}),onSuccess:()=>{close();void refresh();toast.show({tone:"success",message:"Custom object created."});},onError:e=>toast.show({tone:"danger",message:mutationErrorMessage(e)})});
  const archive=useMutation({mutationFn:(apiName:string)=>crm.archiveCustomObjectDefinition({apiName}),onSuccess:()=>{void refresh();toast.show({tone:"success",message:"Custom object archived. Records are retained."});},onError:e=>toast.show({tone:"danger",message:mutationErrorMessage(e)})});
  if(!canRead)return <div><PageHeader title="Custom objects"/><EmptyState title="Administrator access required" description="Custom objects are managed by CRM administrators."/></div>;
  return <div><PageHeader title="Custom objects" actions={canWrite&&<Button onClick={()=>setOpen(true)}>New object</Button>}/><p className="mb-5 max-w-3xl text-sm text-slate-600">Define tenant-owned object types without a schema migration. Add fields from Custom fields, then create records through the API.</p>{query.isPending&&<SkeletonRows rows={4}/>} {query.error&&renderQueryError(query.error,{onRetry:()=>query.refetch(),forbiddenDescription:"You don't have permission to view custom objects."})}{query.data&&objects.length===0&&<EmptyState title="No custom objects" description="Create an object to model data unique to your organization." action={canWrite&&<Button onClick={()=>setOpen(true)}>New object</Button>}/>} {objects.length>0&&<Table caption="Custom objects" rows={objects} getRowKey={o=>o.id} columns={[{key:"label",header:"Object",render:o=><><div className="font-medium">{o.label}</div><div className="text-xs text-slate-500">{o.apiName}</div></>},{key:"plural",header:"Plural",render:o=>o.pluralLabel},{key:"status",header:"Status",render:o=>o.archivedAt?"Archived":"Active"},{key:"actions",header:"Row actions",headerHidden:true,render:o=>canWrite&&!o.archivedAt&&<Button size="sm" variant="secondary" loading={archive.isPending} onClick={()=>{if(window.confirm(`Archive ${o.label}? Existing records will remain available for historical reporting.`))archive.mutate(o.apiName)}}>Archive</Button>} ]}/>}<Dialog open={open} onClose={close} title="New custom object" footer={<><Button variant="secondary" onClick={close}>Cancel</Button><Button loading={create.isPending} disabled={!apiName||!label||!plural} onClick={()=>create.mutate()}>Create object</Button></>}><div className="grid gap-3"><Input label="Singular label" value={label} onChange={e=>setLabel(e.target.value)} required/><Input label="Plural label" value={plural} onChange={e=>setPlural(e.target.value)} required/><Input label="API name" value={apiName} onChange={e=>setApiName(e.target.value.toLowerCase().replace(/[^a-z0-9_]/g,""))} hint="Lowercase letters, digits, and underscores. This cannot be changed later." required/></div></Dialog></div>;
}
