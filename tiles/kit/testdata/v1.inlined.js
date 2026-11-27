(function () {
"use strict";
const state={
face:"",
size:"",
theme:"light",
lang:"en",
data:null,
refs:new Map(),
check:null,
emptyUsed:false,
failed:false,
unresolved:new Set(),
renderMS:0,
};
const BUILT=Symbol("baum.built");
const doc=document;
const DOC=Document.prototype;
const docCall=(name,...args)=>DOC[name].apply(doc,args);
const docGet=(name)=>{
const descriptor=Object.getOwnPropertyDescriptor(DOC,name);
return descriptor?descriptor.get.call(doc):undefined;
};
const listen=(target,type,listener)=>
EventTarget.prototype.addEventListener.call(target,type,listener);
const create=(tag)=>docCall("createElement",tag);
function el(tag,className,text){
const node=create(tag);
if(className)node.className=className;
if(text!==undefined&&text!==null)node.textContent=String(text);
return node;
}
const isRecord=(value)=>
value!==null&&typeof value==="object"&&!Array.isArray(value);
const str=(value)=>(typeof value==="string"?value:"");
const int=(value)=>(Number.isInteger(value)?value:null);
const table=(entries)=>Object.assign(Object.create(null),entries);
const HOLE=`baum${Math.random().toString(36).slice(2, 10)}`;
const ANY_HOLE=new RegExp(`\\{\\{${HOLE}-(\\d+)\\}\\}`,"g");
const HAS_HOLE=new RegExp(`\\{\\{${HOLE}-\\d+\\}\\}`);
const ONLY_HOLE=new RegExp(`^\\{\\{${HOLE}-(\\d+)\\}\\}$`);
const REFUSED=new Set([
"script","iframe","frame","frameset","object","embed","base","meta","link","form",
"portal",
]);
const RAW=new Set(["style","script","title","textarea","xmp","noscript","noembed","noframes"]);
const FIXED=new Set(["srcset","ping","action","formaction","attributename"]);
const RESOURCES=new Set([
"src","poster","background","data","cite","longdesc","lowsrc","dynsrc","manifest",
"codebase","icon","archive","srcset","ping","action","formaction",
]);
const LINKS=new Set(["a","area","use"]);
const templates=new WeakMap();
const FRAGMENT = /^#/;
const IMAGE = /^data:image\/[a-z0-9.+-]+[;,]/i;
const BLOB = /^blob:/i;
const STYLE = /[;:{}()\\"'<>]|\/\*|url/i;
function checkURL(tag,name,value){
const lower=name.toLowerCase();
const link=lower.endsWith("href");
if(!link&&!RESOURCES.has(lower))return;
const ok=link&&LINKS.has(tag)
?FRAGMENT.test(value)
:FRAGMENT.test(value)&&link||IMAGE.test(value)||BLOB.test(value);
if(!ok){
throw new TypeError(
link&&LINKS.has(tag)
?`baum.html: <${tag} ${name}> may only be a #fragment.`
:`baum.html: ${name} may only be a data:image or blob: URL.`,
);
}
}
function checkAttribute(element,name,value){
const lower=name.toLowerCase();
if(lower.startsWith("on")){
throw new TypeError(`baum.html: no ${name}; use addEventListener.`);
}
const tag=element.localName.toLowerCase();
checkURL(tag,name,value);
if ((tag === "animate" || tag === "set") && lower === "attributename" && /^(on|.*href$)/i.test(value)) {
throw new TypeError(`baum.html: <${tag}> may not change ${value}.`);
}
}
function holeKinds(strings){
const kinds=[];
let mode="text";
let quote="";
for(let i=0;i<strings.length;i++){
const s=strings[i];
for(let j=0;j<s.length;j++){
const c=s[j];
if(mode==="text"){
if(c==="<"&&s.startsWith("!--",j+1)){
mode="comment";
j+=3;
} else if (c === "<" && /[a-zA-Z/!?]/.test(s[j + 1] || "")) mode = "tag";
}else if(mode==="comment"){
if(s.startsWith("-->",j)){
mode="text";
j+=2;
}
}else if(mode==="tag"){
if(c==='"'||c==="'"){
mode="quote";
quote=c;
}else if(c===">")mode="text";
}else if(c===quote)mode="tag";
}
if(i===strings.length-1)break;
if(mode==="comment"||(mode==="text"&&s.endsWith("<"))){
throw new TypeError("baum.html: no values in comments or tag names.");
}
kinds.push(mode==="text"?"node":"attr");
}
return kinds;
}
function compile(strings){
const kinds=holeKinds(strings);
let source=strings[0];
for(let i=1;i<strings.length;i++){
const marker=`{{${HOLE}-${i - 1}}}`;
source+=(kinds[i-1]==="node"?`<${"!--"}${marker}-->` : marker) + strings[i];
}
const template=create("template");
template.innerHTML=source;
const parts=[];
const found=new Set();
const walker=docCall("createTreeWalker",template.content,0x85);
let index=-1;
for(let node=walker.nextNode();node;node=walker.nextNode()){
index++;
const parent=node.parentNode&&node.parentNode.localName;
if(node.nodeType!==1){
const match=node.nodeType===8&&ONLY_HOLE.exec(node.data);
if(match&&!RAW.has(String(parent).toLowerCase())){
parts.push({index,node:true,hole:Number(match[1])});
found.add(Number(match[1]));
}else if(HAS_HOLE.test(node.data)){
throw new TypeError(`baum.html: no values in <${parent}>.`);
}
continue;
}
const tag=node.localName.toLowerCase();
if(REFUSED.has(tag))throw new TypeError(`baum.html: no <${tag}> in a tile.`);
if(tag==="template"&&HAS_HOLE.test(node.innerHTML)){
throw new TypeError("baum.html: no values in a <template>; make its copies with baum.html.");
}
for(const attribute of Array.from(node.attributes)){
const{name,value}=attribute;
if(HAS_HOLE.test(name))throw new TypeError("baum.html: no values as attribute names.");
if(!HAS_HOLE.test(value)){
checkAttribute(node,name,value);
continue;
}
if(name.toLowerCase().startsWith("on"))checkAttribute(node,name,"");
if(FIXED.has(name.toLowerCase()))throw new TypeError(`baum.html: ${name} takes no values.`);
const statics=[];
const holes=[];
let last=0;
for(const match of value.matchAll(ANY_HOLE)){
statics.push(value.slice(last,match.index));
holes.push(Number(match[1]));
found.add(Number(match[1]));
last=match.index+match[0].length;
}
statics.push(value.slice(last));
parts.push({index,name,ns:attribute.namespaceURI,local:attribute.localName,statics,holes});
}
}
if(found.size!==strings.length-1){
throw new TypeError("baum.html: a value is out of place.");
}
return{template,parts};
}
function describe(value){
if(value===null)return"null";
if(Array.isArray(value))return"an array";
return typeof value==="object"?"an object":`a ${typeof value}`;
}
function insert(parent,before,value,depth){
if(value===null||value===undefined||typeof value==="boolean")return;
if(typeof value==="string"||typeof value==="number"||typeof value==="bigint"){
parent.insertBefore(docCall("createTextNode",String(value)),before);
}else if(value instanceof Node&&value.nodeType!==9){
parent.insertBefore(value,before);
}else if(typeof value==="object"&&typeof value[Symbol.iterator]==="function"){
if(depth>32)throw new TypeError("baum.html: arrays nest too deep.");
for(const item of value)insert(parent,before,item,depth+1);
}else{
throw new TypeError(`baum.html can't show ${describe(value)}: pass text, numbers or nodes.`);
}
}
function attributeText(value,style){
if(value===null||value===undefined||typeof value==="boolean")return"";
if(typeof value==="string"||typeof value==="number"||typeof value==="bigint"){
const text=String(value);
if(style&&STYLE.test(text)){
throw new TypeError("baum.html: a style value may only be a word or a number, as in style=\"--value:${n}\".");
}
return text;
}
if(Array.isArray(value))return value.map((item)=>attributeText(item,style)).filter(Boolean).join(" ");
throw new TypeError(`baum.html can't put ${describe(value)} in an attribute.`);
}
function html(strings,...values){
if(!Array.isArray(strings)||!Array.isArray(strings.raw)){
throw new TypeError("baum.html is a tag: baum.html`<p>${text}</p>`.");
}
let compiled=templates.get(strings);
if(!compiled){
compiled=compile(strings);
templates.set(strings,compiled);
}
const fragment=docCall("importNode",compiled.template.content,true);
if(compiled.parts.length===0)return fragment;
const nodes=[];
const wanted=new Set(compiled.parts.map((part)=>part.index));
const walker=docCall("createTreeWalker",fragment,0x85);
let index=-1;
for(let node=walker.nextNode();node;node=walker.nextNode()){
index++;
if(wanted.has(index))nodes[index]=node;
}
for(const part of compiled.parts){
const node=nodes[part.index];
if(part.node){
insert(node.parentNode,node,values[part.hole],0);
node.remove();
continue;
}
const{name,ns,local,statics,holes}=part;
const whole=statics.length===2&&statics[0]===""&&statics[1]==="";
const only=values[holes[0]];
if(whole&&(only===null||only===undefined||only===false)){
if(ns)node.removeAttributeNS(ns,local);
else node.removeAttribute(name);
continue;
}
const style=name.toLowerCase()==="style";
let text=statics[0];
for(let i=0;i<holes.length;i++)text+=attributeText(values[holes[i]],style)+statics[i+1];
checkAttribute(node,name,text);
if(ns)node.setAttributeNS(ns,name,text);
else node.setAttribute(name,text);
}
return fragment;
}
const KEY_FIELDS=table({
hunk:["id","lines?"],
file:["path"],
commit:["sha"],
pr:["number"],
pr_body:["section?"],
pr_comment:["id"],
review_thread:["id"],
prompt:["session","index"],
session_event:["session","event"],
check:["id","test?"],
bot_report:["id"],
issue:["system","key"],
spec:["page","assertion?"],
external_url:["url"],
attachment:["id?","url?"],
risk:["id"],
evidence:["id"],
change:["id"],
tile:["id"],
});
function keyOf(c){
if(!isRecord(c)||typeof c.type!=="string")return null;
const fields=KEY_FIELDS[c.type];
if(!fields)return null;
const key=[c.type];
for(const field of fields){
const optional=field.endsWith("?");
let value=c[optional?field.slice(0,-1):field];
if(field==="lines?"){
value=Array.isArray(value)&&value.length===2&&value.every(Number.isInteger)?[value[0],value[1]]:null;
}else if(typeof value!=="string"&&!Number.isFinite(value)){
if(!optional||(value!==undefined&&value!==null))return null;
value=null;
}
key.push(value);
}
if(c.type==="attachment"&&key[1]===null&&key[2]===null)return null;
return JSON.stringify(key);
}
function resolve(citation){
const key=keyOf(citation);
const payload=key===null?undefined:state.refs.get(key);
return{key,payload:isRecord(payload)?payload:null};
}
const REASONS=table({
not_in_review:"not in this review",
comparing:"comparing versions",
too_many:"too many citations",
load_failed:"couldn't be loaded",
});
const reason=(payload)=>REASONS[str(payload&&payload.why)]||"not available";
const baseName = (path) => path.slice(path.lastIndexOf("/") + 1);
function rangeText(citation,payload){
const range=Array.isArray(citation.lines)?citation.lines:payload.range;
if(!Array.isArray(range)||int(range[0])===null)return"";
return int(range[1])===null||range[1]===range[0]?`${range[0]}`:`${range[0]}–${range[1]}`;
}
const TIERS=new Set(["critical","high","medium","low"]);
const EVIDENCE_TONES=table({
verified:"positive",
observed:"info",
failed:"critical",
missing:"caution",
});
const CHECK_TONES=table({
success:"positive",
failure:"critical",
timed_out:"critical",
action_required:"caution",
});
function citeText(citation,payload){
const label=str(payload.label);
switch(citation.type){
case"hunk":{
const path=str(payload.path);
if(!path)return{text:label,title:label};
const range=rangeText(citation,payload);
return{text:range?`${baseName(path)}:${range}`:baseName(path),title:range?`${path}:${range}`:path};
}
case"file":{
const path=str(payload.path)||str(citation.path);
return{text:baseName(path)||label,title:path||label};
}
case"commit":{
const sha=str(payload.sha)||str(citation.sha);
return{text:sha.slice(0,7)||label,title:str(payload.subject)||label};
}
case"risk":
return{text:label,title:str(payload.text)||label};
case"evidence":
return{text:label,title:str(payload.claim)||label};
default:
return{text:label,title:label};
}
}
function citeMark(kind,payload){
const mark=el("span","baum-mark");
mark.setAttribute("aria-hidden","true");
if(kind==="risk"&&TIERS.has(payload.tier))mark.dataset.tier=payload.tier;
else if(kind==="evidence"&&EVIDENCE_TONES[payload.evidence_status]){
mark.dataset.tone=EVIDENCE_TONES[payload.evidence_status];
}else if(kind==="check"&&CHECK_TONES[payload.conclusion]){
mark.dataset.tone=CHECK_TONES[payload.conclusion];
}else return null;
return mark;
}
function kindOf(citation){
return isRecord(citation) && /^[a-z_]{1,24}$/.test(citation.type) ? citation.type : "unknown";
}
function cite(citation,label){
const node=create("baum-cite");
node[BUILT]=true;
const kind=kindOf(citation);
node.dataset.kind=kind;
const{key,payload}=resolve(citation);
const own=typeof label==="string"&&label.trim()?label:null;
if(!payload||payload.status!=="ok"){
if(!payload)state.unresolved.add(key??"invalid citation");
node.dataset.state="unavailable";
const chip=el("span","baum-chip");
const text=payload?own||str(payload.label)||"Citation unavailable":"Citation unavailable";
chip.append(el("span","baum-label",text));
chip.title=payload?`Unavailable: ${reason(payload)}`:"This citation isn't in the review.";
node.append(chip);
return node;
}
node.dataset.state="ok";
const{text,title}=citeText(citation,payload);
const interactive=state.face==="detail"&&payload.navigable===true;
const chip=el(interactive?"button":"span","baum-chip");
if(interactive){
chip.type="button";
chip.addEventListener("click",()=>post("jump",{key}));
}
const mark=citeMark(kind,payload);
if(mark)chip.append(mark);
chip.append(el("span","baum-label",own||text||"Citation"));
if(title&&title!==(own||text))chip.title=title;
node.append(chip);
return node;
}
const MARKS=table({add:"+",remove:"−",context:" "});
const SYNTAX=new Set([
"keyword","string","string-expression","comment","constant","function","parameter",
"punctuation","link","inserted","deleted","changed",
]);
function codeCell(line){
const cell=el("td","baum-code");
const text=str(line.text);
const tokens=Array.isArray(line.tokens)?line.tokens:null;
const fits=
tokens&&
tokens.every((token)=>Array.isArray(token)&&typeof token[0]==="string")&&
tokens.map((token)=>token[0]).join("")===text;
if(!fits){
cell.textContent=text;
return cell;
}
for(const[content,kind]of tokens){
if(SYNTAX.has(kind)){
const span=el("span","",content);
span.style.color=`var(--syntax-token-${kind})`;
cell.append(span);
}else cell.append(content);
}
return cell;
}
function lineRow(line){
const kind=line.kind==="add"||line.kind==="remove"?line.kind:"context";
const row=el("tr");
row.dataset.kind=kind;
row.append(
el("td","baum-n",kind==="add"?"":int(line.old)??""),
el("td","baum-n",kind==="remove"?"":int(line.new)??""),
el("td","baum-m",MARKS[kind]),
codeCell(line),
);
return row;
}
function noteRow(className,text){
const row=el("tr",className);
const cell=el("td","",text);
cell.colSpan=4;
row.append(cell);
return row;
}
const plural=(count,one)=>`${count} ${one}${count === 1 ? "" : "s"}`;
function diffHead(citation,payload,key){
const head=el("div","baum-diff-head");
const path=str(payload&&payload.path)||str(citation.path);
const range=payload&&citation.type==="hunk"?rangeText(citation,payload):"";
const navigable=state.face==="detail"&&payload&&payload.status==="ok"&&payload.navigable===true;
const name=el(navigable?"button":"span","baum-diff-path");
if(navigable){
name.type="button";
name.addEventListener("click",()=>post("jump",{key}));
}
const slash = path.lastIndexOf("/") + 1;
const file=el("span");
file.append(el("span","",path.slice(slash)+(range?`:${range}`:"")));
name.append(el("span","",path.slice(0,slash)),file);
if(!path)file.firstChild.textContent=str(payload&&payload.label)||"Code";
name.title=path+(range?`:${range}`:"");
head.append(name);
if(payload&&payload.status==="ok"&&int(payload.additions)!==null&&int(payload.deletions)!==null){
const stat=el("span","baum-diff-stat");
stat.append(el("span","",`+${payload.additions}`)," ",el("span","",`−${payload.deletions}`));
head.append(stat);
}
return head;
}
function diff(citation,options){
const max=isRecord(options)&&int(options.maxLines)!==null?options.maxLines:40;
const maxLines=Math.max(1,Math.min(max,1000));
const node=create("baum-diff");
node[BUILT]=true;
node.dataset.face=state.face==="detail"?"detail":"glance";
const box=el("div","baum-diff");
node.append(box);
const{key,payload}=resolve(citation);
const unavailable=(why)=>{
node.dataset.state="unavailable";
box.append(el("div","baum-diff-note",`Diff unavailable (${why})`));
return node;
};
if(!payload){
state.unresolved.add(key??"invalid citation");
return unavailable("the citation isn't in the review");
}
box.append(diffHead(citation,payload,key));
if(payload.status!=="ok")return unavailable(reason(payload));
if(citation.type!=="hunk"&&citation.type!=="file")return unavailable("not code");
node.dataset.state="ok";
if(node.dataset.face==="glance"||citation.type==="file")return node;
const excerpt=payload.excerpt;
if(!isRecord(excerpt)||!Array.isArray(excerpt.lines))return unavailable("no excerpt");
const body=el("div","baum-diff-body");
const table=el("table");
const cols=el("colgroup");
cols.append(el("col","baum-n"),el("col","baum-n"),el("col","baum-m"),el("col"));
const shown=el("tbody");
if(str(excerpt.header))shown.append(noteRow("baum-hunk",excerpt.header));
const before=int(excerpt.omitted_before);
if(before)shown.append(noteRow("baum-gap",`⋯ ${plural(before, "line")} above`));
const lines=excerpt.lines.filter(isRecord);
for(const line of lines.slice(0,maxLines))shown.append(lineRow(line));
table.append(cols,shown);
const after=int(excerpt.omitted_after);
const tail=el("tbody");
for(const line of lines.slice(maxLines))tail.append(lineRow(line));
if(after)tail.append(noteRow("baum-gap",`⋯ ${plural(after, "line")} below`));
if(tail.firstChild)table.append(tail);
body.append(table);
box.append(body);
const rest=lines.length-maxLines;
if(rest>0){
tail.hidden=true;
const more=el("button","baum-more",`Show ${plural(rest, "more line")}`);
more.type="button";
more.setAttribute("aria-expanded","false");
more.addEventListener("click",()=>{
tail.hidden=!tail.hidden;
more.setAttribute("aria-expanded",String(!tail.hidden));
more.textContent=tail.hidden?`Show ${plural(rest, "more line")}` : "Show fewer lines";
});
box.append(more);
}
return node;
}
function keptWords(a,b){
const words = (pieces) => pieces.flatMap((piece, at) => (/^\s+$/.test(piece) ? [] : [at]));
const[wa,wb]=[words(a),words(b)];
const lengths=Array.from({length:wa.length+1},()=>new Array(wb.length+1).fill(0));
for(let i=wa.length-1;i>=0;i--)
for(let j=wb.length-1;j>=0;j--)
lengths[i][j]=
a[wa[i]]===b[wb[j]]
?lengths[i+1][j+1]+1
:Math.max(lengths[i+1][j],lengths[i][j+1]);
const keptA=new Set();
const keptB=new Set();
for(let i=0,j=0;i<wa.length&&j<wb.length;){
if(a[wa[i]]===b[wb[j]]){
keptA.add(wa[i++]);
keptB.add(wb[j++]);
}else if(lengths[i+1][j]>=lengths[i][j+1])i++;
else j++;
}
const common=keptA.size>0&&keptA.size*2>=Math.min(wa.length,wb.length);
return{common,keptA,keptB};
}
function markedText(pieces,kept,marking,tag,className){
const node=el("span");
const marked=(at)=>
marking && at >= 0 && at < pieces.length && !kept.has(at) && !/^\s+$/.test(pieces[at]);
let run=null;
for(const[at,text]of pieces.entries()){
if(marked(at)||(marked(at-1)&&marked(at+1))){
if(!run){
run=el(tag,className);
node.append(run);
}
run.append(text);
}else{
run=null;
node.append(text);
}
}
return node;
}
function compare(was,now){
const before=str(was);
const after=str(now);
const split = (text) => text.match(/\s+|\S+/g) ?? [];
const[a,b]=[split(before),split(after)];
const{common,keptA,keptB}=
a.length*b.length<=40000?keptWords(a,b):{common:false,keptA:new Set(),keptB:new Set()};
const same=before===after;
const marking=common&&!same&&before!==""&&after!=="";
return{
was:markedText(a,keptA,marking,"del","kit-del"),
now:markedText(b,keptB,marking,"ins","kit-ins"),
same,
};
}
function empty(text){
state.emptyUsed=true;
const node=el("p","kit-empty",typeof text==="string"&&text?text:"Nothing to show.");
return node;
}
function fail(message){
const text=(typeof message==="string"&&message)||"This tile couldn't be drawn.";
state.failed=true;
report("render_failed",text);
const node=el("p","kit-fail",text);
node.setAttribute("role","alert");
return node;
}
function defineElement(tag,factory){
if(!window.customElements||customElements.get(tag))return;
customElements.define(
tag,
class extends HTMLElement{
connectedCallback(){
if(this[BUILT])return;
this[BUILT]=true;
this.dataset.state="unavailable";
this.textContent=`Use baum.${factory}() to make <${tag}>.`;
console.error(this.textContent);
}
},
);
}
const MD_LIMIT=20000;
const MD_DEPTH=8;
const MD_URL=1000;
const FENCE = /^ {0,3}(`{3,}|~{3,})/;
const QUOTE = /^ {0,3}> ?/;
const ITEM = /^( {0,3})([-*+]|\d{1,9}[.)])[ \t]+(.*)$/;
const BLANK = /^[ \t]*$/;
const SPACE=(c)=>c===" "||c==="\t";
function heading(line){
const match = /^ {0,3}(#{1,6})(?:[ \t]|$)/.exec(line);
if(!match)return null;
const start=match[0].length;
let end=line.length;
while(end>start&&SPACE(line[end-1]))end--;
let close=end;
while(close>start&&line[close-1]==="#")close--;
if(close<end&&(close===start||SPACE(line[close-1])))end=close;
return{level:match[1].length,text:line.slice(start,end).trim()};
}
function isRule(line){
if (!/^ {0,3}[-*_]/.test(line)) return false;
const marks = line.replace(/[ \t]/g, "");
return marks.length>=3&&[...marks].every((mark)=>mark===marks[0]);
}
const startsBlock=(line)=>
FENCE.test(line)||heading(line)!==null||isRule(line)||QUOTE.test(line)||ITEM.test(line);
function nextFilled(lines,from){
for(let i=from;i<lines.length;i++)if(!BLANK.test(lines[i]))return i;
return-1;
}
function inline(parent,text,depth){
let buffer="";
const flush=()=>{
if(buffer)parent.append(buffer);
buffer="";
};
const unclosed=new Set();
const closer=(delimiter,from)=>{
if(unclosed.has(delimiter))return-1;
for(let at=text.indexOf(delimiter,from);at>=0;at=text.indexOf(delimiter,at+1)){
if (!/\s/.test(text[at - 1])) return at;
}
unclosed.add(delimiter);
return-1;
};
let closeAt=-2;
let openAt=-2;
let parenAt=-2;
let i=0;
while(i<text.length){
const c=text[i];
if (c === "\\" && /[\\`*_{}[\]()#+\-.!~<>|]/.test(text[i + 1] || "")) {
buffer+=text[i+1];
i+=2;
continue;
}
if(c==="`"){
let run=1;
while(text[i+run]==="`")run++;
const fence="`".repeat(run);
let end=unclosed.has(fence)?-1:text.indexOf(fence,i+run);
while(end>=0&&text[end+run]==="`")end=text.indexOf(fence,end+run+1);
if(end<0){
unclosed.add(fence);
buffer+=fence;
i+=run;
continue;
}
let code=text.slice(i+run,end);
if(code.length>2&&code.startsWith(" ")&&code.endsWith(" "))code=code.slice(1,-1);
flush();
parent.append(el("code","",code));
i=end+run;
continue;
}
const pair=text.slice(i,i+2);
const open=(delimiter,tag)=>{
const start=i+delimiter.length;
if (/\s/.test(text[start] || " ")) return false;
if (delimiter[0] === "_" && /[\p{L}\p{N}]/u.test(text[i - 1] || "")) return false;
const end=closer(delimiter,start+1);
if(end<0)return false;
flush();
const node=el(tag);
if(depth<MD_DEPTH)inline(node,text.slice(start,end),depth+1);
else node.textContent=text.slice(start,end);
parent.append(node);
i=end+delimiter.length;
return true;
};
if((pair==="**"||pair==="__")&&open(pair,"strong"))continue;
if(pair==="~~"&&open(pair,"del"))continue;
if((c==="*"||c==="_")&&pair!=="**"&&pair!=="__"&&open(c,"em"))continue;
if(c==="["){
if(closeAt===-2||(closeAt>=0&&closeAt<=i))closeAt=text.indexOf("](",i+1);
if(openAt===-2||(openAt>=0&&openAt<=i))openAt=text.indexOf("[",i+1);
let end=-1;
const close=closeAt>=0&&(openAt<0||openAt>closeAt)?closeAt:-1;
if(close>i&&(parenAt===-2||(parenAt>=0&&parenAt<close+2))){
parenAt=text.indexOf(")",close+2);
}
const limit=parenAt<0?-1:Math.min(text.length,close+2+MD_URL);
for(let j=close+2,depth=0;close>i&&j<limit&&depth<=32&&text.charCodeAt(j)>32;j++){
if(text[j]==="(")depth++;
else if(text[j]===")"&&depth--===0){
end=j;
break;
}
}
const label=end<0?"":text.slice(i+1,close);
const url=end<0?"":text.slice(close+2,end);
if(label&&url){
flush();
const link=el("span","baum-link");
link.title=url;
if(depth<MD_DEPTH)inline(link,label,depth+1);
else link.textContent=label;
parent.append(link);
i=end+1;
continue;
}
}
buffer+=c;
i++;
}
flush();
}
function paragraph(parent,lines,depth){
const node=el("p");
lines.forEach((line,index)=>{
if(index>0)node.append(el("br"));
inline(node,line.trim(),depth);
});
parent.append(node);
}
function list(parent,lines,start,depth){
const first=ITEM.exec(lines[start]);
const ordered = /\d/.test(first[2]);
const node=el(ordered?"ol":"ul");
const number=parseInt(first[2],10);
if(ordered&&number!==1)node.start=number;
const sameKind = (match) => match && /\d/.test(match[2]) === ordered && match[1].length <= first[1].length + 1;
let i=start;
while(i<lines.length){
const match=ITEM.exec(lines[i]);
if(!sameKind(match))break;
const indent=match[1].length+match[2].length+1;
const body=[match[3]];
i++;
while(i<lines.length){
const line=lines[i];
if(BLANK.test(line)){
const next=nextFilled(lines,i);
if (next < 0 || !(/^\s{2,}/.test(lines[next]) || sameKind(ITEM.exec(lines[next])))) break;
while(i<next){
body.push("");
i++;
}
continue;
}
if(sameKind(ITEM.exec(line)))break;
if (!/^\s/.test(line) && startsBlock(line)) break;
body.push(line.replace(new RegExp(`^ {0,${indent}}`),""));
i++;
}
const item=el("li");
blocks(item,body,depth+1);
if(item.childNodes.length===1&&item.firstChild.nodeName==="P"){
item.replaceChildren(...item.firstChild.childNodes);
}
node.append(item);
if(i<lines.length&&BLANK.test(lines[i])){
const next=nextFilled(lines,i);
if(next>=0&&sameKind(ITEM.exec(lines[next])))i=next;
}
}
parent.append(node);
return i;
}
function blocks(parent,lines,depth){
if(depth>MD_DEPTH){
paragraph(parent,lines.filter((line)=>!BLANK.test(line)),depth);
return;
}
let i=0;
while(i<lines.length){
const line=lines[i];
let match;
if(BLANK.test(line)){
i++;
}else if((match=FENCE.exec(line))){
const marker=match[1];
const body=[];
i++;
while(i<lines.length&&!new RegExp(`^ {0,3}${marker[0]}{${marker.length},}[ \\t]*$`).test(lines[i])){
body.push(lines[i]);
i++;
}
i++;
const pre=el("pre");
pre.append(el("code","",body.join("\n")));
parent.append(pre);
}else if((match=heading(line))){
const{level}=match;
const node=el(level<=2?"p":level===3?"h3":"h4",level<=2?"kit-strong":"");
inline(node,match.text,depth);
parent.append(node);
i++;
}else if(isRule(line)){
parent.append(el("hr"));
i++;
}else if(QUOTE.test(line)){
const body=[];
while(i<lines.length&&QUOTE.test(lines[i])){
body.push(lines[i].replace(QUOTE,""));
i++;
}
const quote=el("blockquote");
blocks(quote,body,depth+1);
parent.append(quote);
}else if(ITEM.test(line)){
i=list(parent,lines,i,depth);
}else{
const body=[line];
i++;
while(i<lines.length&&!BLANK.test(lines[i])&&!startsBlock(lines[i])){
body.push(lines[i]);
i++;
}
paragraph(parent,body,depth);
}
}
}
function flatten(source){
return source
.split("\n")
.filter((line)=>!FENCE.test(line)&&!isRule(line))
.map((line)=>
line
.replace(/^ {0,3}(?:> ?)+/, "")
.replace(/^ {0,3}#{1,6}[ \t]+/, "")
.replace(/^ {0,3}(?:[-*+]|\d{1,9}[.)])[ \t]+/, "")
.trim(),
)
.filter(Boolean)
.join(" ");
}
function md(text,options){
const node=create("baum-md");
node[BUILT]=true;
const source=(typeof text==="string"?text:text==null?"":String(text))
.slice(0,MD_LIMIT)
.replace(/\r\n?/g, "\n");
if(isRecord(options)&&options.inline){
node.dataset.inline="";
inline(node,flatten(source),0);
}else{
blocks(node,source.split("\n"),0);
}
return node;
}
const KIT=1;
const PROTOCOL = "baum.tile/1";
const AUTHOR_LINE = /*@AUTHOR_LINE@*/ 0;
const RENDER_TIMEOUT=3000;
const MAX_ERRORS=10;
const MAX_TEXT=500;
const MAX_CHARS=1500000;
const MAX_NODES=200000;
const MAX_DEPTH=64;
const KEYS=new Set(["Escape","u","j","k","ArrowLeft","ArrowRight","[","]","?"]);
const FACES=new Set(["glance","detail"]);
const SIZES=new Set(["full","half","third"]);
const THEMES=new Set(["light","dark"]);
const THEME_TOKENS=new Set([
"--color-ink","--color-paper","--color-shadow","--brand-forest","--brand-sand-100",
"--brand-green-600","--color-critical","--color-blue","--series-1","--series-2",
"--series-3","--series-4","--series-5","--series-6","--series-7","--series-8",
"--series-caramel","--series-sage","--series-espresso","--surface-page","--surface-card",
"--surface-canvas","--surface-popup","--text-primary","--text-secondary",
"--text-inverse","--border-default","--border-focus","--action-primary",
"--action-primary-hover","--nav-hover","--nav-selected","--status-positive",
"--status-caution","--status-critical",
]);
const MAX_TOKEN_VALUE=64;
const LANG = /^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8}){0,4}$/;
const host=window.parent;
const framed=host!==window;
const consoleError=console.error;
let spec=null;
let started=false;
let helloSent=false;
let renderSeq=0;
let readySent=false;
const errorsSent=table({});
function post(kind,fields){
if(!framed)return;
try{
host.postMessage(Object.assign({protocol:PROTOCOL,kind},fields),"*");
}catch{
}
}
const clip=(text)=>(text.length>MAX_TEXT?`${text.slice(0, MAX_TEXT - 1)}…`:text);
function describeError(value){
try{
if(value instanceof Error)return`${value.name}: ${value.message}`;
if(typeof value==="string")return value;
return JSON.stringify(value)??String(value);
}catch{
try{
return String(value);
}catch{
return"(a value that can't be shown)";
}
}
}
function stackPosition(error){
let stack="";
try{
stack=error&&typeof error.stack==="string"?error.stack:"";
}catch{
}
for(const line of stack.split("\n")){
const match = /:(\d+):(\d+)\)?\s*$/.exec(line);
if(match&&Number(match[1])>AUTHOR_LINE){
return{line:Number(match[1])-AUTHOR_LINE,column:Number(match[2])};
}
}
return null;
}
function linePosition(line,column){
return Number.isInteger(line)&&line>AUTHOR_LINE
?{line:line-AUTHOR_LINE,column:Number.isInteger(column)?column:undefined}
:null;
}
function report(code,message,where){
if((errorsSent[code]||0)>=MAX_ERRORS)return;
errorsSent[code]=(errorsSent[code]||0)+1;
if(typeof message==="function")message=message();
if(typeof where==="function")where=where();
const fields={code,message:clip(String(message||code))};
if(where){
if(where.line!==undefined)fields.line=where.line;
if(where.column!==undefined)fields.column=where.column;
if(where.blocked!==undefined)fields.blocked=where.blocked;
}
post("error",fields);
}
listen(window,"error",(event)=>{
if(!(event instanceof ErrorEvent))return;
report(
"script_error",
()=>event.message||describeError(event.error),
()=>stackPosition(event.error)||linePosition(event.lineno,event.colno),
);
});
listen(window,"unhandledrejection",(event)=>{
report("unhandled_rejection",()=>describeError(event.reason),()=>stackPosition(event.reason));
});
listen(doc,"securitypolicyviolation",(event)=>{
let blocked=String(event.blockedURI||"");
try{
const url=new URL(blocked);
blocked = url.host ? `${url.protocol}//${url.host}` : url.protocol;
}catch{
}
const directive=event.effectiveDirective||event.violatedDirective||"policy";
const where=linePosition(event.lineNumber,event.columnNumber)||{};
where.blocked=blocked;
report("csp_violation",`The frame's ${directive} policy blocked ${blocked || "a request"}.`, where);
});
console.error=function error(...args){
report("console_error",()=>args.map(describeError).join(" "));
return consoleError.apply(console,args);
};
function plainJSON(value){
const stack=[[value,0]];
let nodes=0;
let chars=0;
while(stack.length>0){
const[item,depth]=stack.pop();
if(++nodes>MAX_NODES||depth>MAX_DEPTH)return false;
if(item===null||typeof item==="boolean")continue;
if(typeof item==="number"){
if(!Number.isFinite(item))return false;
}else if(typeof item==="string"){
chars+=item.length;
if(chars>MAX_CHARS)return false;
}else if(Array.isArray(item)){
for(const child of item)stack.push([child,depth+1]);
}else if(typeof item==="object"){
const proto=Object.getPrototypeOf(item);
if(proto!==Object.prototype&&proto!==null)return false;
for(const key of Object.keys(item)){
chars+=key.length;
stack.push([item[key],depth+1]);
}
}else return false;
}
return true;
}
function readRefs(refs){
const map=new Map();
for(const[key,payload]of Object.entries(refs)){
if(
isRecord(payload)&&
(payload.status==="ok"||payload.status==="unavailable")&&
typeof payload.label==="string"
){
map.set(key,payload);
}
}
return map;
}
const CSS_NUMBER = /^[+-]?(\d+(\.\d+)?|\.\d+)([eE][+-]?\d+)?$/;
const COLOR_FUNCTION = /^(rgba?|hsla?|oklch)\(([^()]*)\)$/;
const ASCII_SPACE = /[ \t\n\r\f]+/;
const trimASCII = (text) => text.replace(/^[ \t\n\r\f]+|[ \t\n\r\f]+$/g, "");
const fieldsASCII=(text)=>text.split(ASCII_SPACE).filter((part)=>part!=="");
function cssFloat(text){
if(!CSS_NUMBER.test(text))return null;
const value=Number(text);
return Number.isFinite(value)?value:null;
}
function cssNumberOrPercent(text,full){
if(!text.endsWith("%"))return cssFloat(text);
const value=cssFloat(text.slice(0,-1));
if(value===null)return null;
const scaled = (value / 100) * full;
return Number.isFinite(scaled)?scaled:null;
}
function cssPercent(text){
if(!text.endsWith("%"))return null;
const value=cssFloat(text.slice(0,-1));
if(value===null)return null;
const scaled = value / 100;
return Number.isFinite(scaled)?scaled:null;
}
function cssHue(text){
let scale=1;
if(text.endsWith("deg"))text=text.slice(0,-3);
else if(text.endsWith("grad"))(text=text.slice(0,-4)),(scale=0.9);
else if(text.endsWith("turn"))(text=text.slice(0,-4)),(scale=360);
else if (text.endsWith("rad")) (text = text.slice(0, -3)), (scale = 180 / Math.PI);
const value=cssFloat(text);
return value!==null&&Number.isFinite(value*scale);
}
function isColor(text){
text=trimASCII(text).toLowerCase();
if(text.startsWith("#")){
return /^(?:[0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})$/.test(text.slice(1));
}
const match=COLOR_FUNCTION.exec(text);
if(!match)return false;
const name=match[1];
const body=trimASCII(match[2]);
let fields;
let alpha="";
const legacy=body.includes(",");
if(legacy){
if (body.includes("/") || name === "oklch") return false;
fields=[];
for(let part of body.split(",")){
part=trimASCII(part);
if(part===""||fieldsASCII(part).length!==1)return false;
fields.push(part);
}
if(fields.length===4)alpha=fields.pop();
}else{
let colour=body;
const slash = body.indexOf("/");
if(slash>=0){
colour=body.slice(0,slash);
alpha=trimASCII(body.slice(slash+1));
if(alpha===""||fieldsASCII(alpha).length!==1)return false;
}
fields=fieldsASCII(colour);
}
if(fields.length!==3)return false;
if(alpha!==""&&cssNumberOrPercent(alpha,1)===null)return false;
if(name==="rgb"||name==="rgba"){
let percents=0;
for(const field of fields){
if(field.endsWith("%"))percents++;
if(cssNumberOrPercent(field,255)===null)return false;
}
return!(legacy&&percents!==0&&percents!==3);
}
if(name==="hsl"||name==="hsla"){
return cssHue(fields[0])&&cssPercent(fields[1])!==null&&cssPercent(fields[2])!==null;
}
return(
cssNumberOrPercent(fields[0],1)!==null&&
cssNumberOrPercent(fields[1],0.4)!==null&&
cssHue(fields[2])
);
}
function readTokens(tokens){
if(!isRecord(tokens))return null;
const entries=[];
for(const token of Object.keys(tokens)){
const value=tokens[token];
if(!THEME_TOKENS.has(token)||typeof value!=="string")return null;
if(value.length>MAX_TOKEN_VALUE||!isColor(value))return null;
entries.push([token,value]);
}
return entries;
}
let themeTokens=[];
function applyTheme(theme,tokens){
state.theme=theme;
const root=docGet("documentElement");
root.dataset.theme=theme;
root.style.colorScheme=theme;
const next=table({});
for(const[token,value]of tokens){
root.style.setProperty(token,value);
next[token]=true;
}
for(const token of themeTokens){
if(!next[token])root.style.removeProperty(token);
}
themeTokens=tokens.map(([token])=>token);
}
function onInit(message){
const{face,size,theme,lang,data,refs,check}=message;
const tokens=message.tokens===undefined?[]:readTokens(message.tokens);
if(tokens===null)return;
if(started||!FACES.has(face)||!SIZES.has(size)||!THEMES.has(theme))return;
if(typeof lang!=="string"||!LANG.test(lang)||!("data"in message)||!isRecord(refs))return;
if(check!==undefined&&!(isRecord(check)&&typeof check.id==="string"))return;
started=true;
Object.assign(state,{face,size,lang,data,refs:readRefs(refs),check:check||null});
const root=docGet("documentElement");
root.lang=lang;
root.dataset.face=face;
root.dataset.size=size;
applyTheme(theme,tokens);
if(face==="detail")watchHeight();
if(spec)render();
}
function onTheme(message){
if(!started||!THEMES.has(message.theme))return;
const tokens=message.tokens===undefined?[]:readTokens(message.tokens);
if(tokens!==null)applyTheme(message.theme,tokens);
}
function onUpdate(message){
if(!started||!("data"in message)||!isRecord(message.refs))return;
state.data=message.data;
state.refs=readRefs(message.refs);
if(spec)render();
}
listen(window,"message",(event)=>{
if(!framed||event.source!==host)return;
const message=event.data;
if(!isRecord(message)||message.protocol!==PROTOCOL||!plainJSON(message))return;
if(message.kind==="init")onInit(message);
else if(message.kind==="update")onUpdate(message);
else if(message.kind==="theme")onTheme(message);
else if(message.kind==="measure"&&state.check&&readySent)post("resize",{height:contentHeight()});
else if(message.kind==="ping")post("pong",{});
});
const TIMEOUT=Symbol("timeout");
function frames(count){
return new Promise((resolve)=>{
const timer=setTimeout(resolve,100);
const step=(left)=>
left===0
?(clearTimeout(timer),resolve())
:requestAnimationFrame(()=>step(left-1));
step(count);
});
}
function turn(){
if(typeof MessageChannel!=="function")return Promise.resolve();
return new Promise((resolve)=>{
const channel=new MessageChannel();
channel.port1.onmessage=()=>{
channel.port1.close();
resolve();
};
channel.port2.postMessage(null);
});
}
async function settle(){
if(state.check){
const fonts=docGet("fonts");
if(fonts&&fonts.status==="loading")await fonts.ready;
await turn();
return;
}
await frames(2);
const fonts=docGet("fonts");
if(fonts&&fonts.status==="loading"){
await Promise.race([fonts.ready,new Promise((resolve)=>setTimeout(resolve,500))]);
await frames(1);
}
}
function visibleText(root){
let count=0;
const walker=docCall("createTreeWalker",root,4);
for(let node=walker.nextNode();node;node=walker.nextNode()){
const parent=node.parentNode&&node.parentNode.nodeName;
if(parent!=="SCRIPT"&&parent!=="STYLE"&&parent!=="TEMPLATE"){
count+=node.data.trim().length;
}
}
return count;
}
function contentHeight(){
return Math.ceil(docGet("documentElement").getBoundingClientRect().height);
}
function measure(root){
const html=docGet("documentElement");
let media=0;
for(const node of root.querySelectorAll("img, svg, canvas, video")){
const box=node.getBoundingClientRect();
if(box.width>=16&&box.height>=16)media++;
}
return{
height:state.face==="glance"?Math.ceil(root.scrollHeight):contentHeight(),
text_chars:visibleText(root),
elements:root.getElementsByTagName("*").length,
media,
empty_state:state.emptyUsed,
failed:state.failed,
unresolved:state.unresolved.size,
overflow_x:html.scrollWidth>html.clientWidth+2,
overflow_y:state.face==="glance"&&root.scrollHeight>root.clientHeight+2,
render_ms:state.renderMS,
};
}
function disclosuresOf(root){
return[...root.querySelectorAll("details")].map((node)=>({
label:summaryText(node),
open:node.open,
}));
}
function summaryText(node){
const summary=node.querySelector(":scope > summary");
return summary?summary.textContent.trim():"";
}
function keepDisclosures(root,before){
const after=root.querySelectorAll("details");
for(let i=0;i<after.length&&i<before.length;i++){
const node=after[i];
if(summaryText(node)===before[i].label&&node.open!==before[i].open)node.open=before[i].open;
}
}
async function render(){
const seq=++renderSeq;
const began=performance.now();
Object.assign(state,{emptyUsed:false,failed:false,unresolved:new Set()});
const root=el("div");
root.id="baum-root";
const old=docCall("getElementById","baum-root");
const disclosures=old?disclosuresOf(old):null;
if(old)old.replaceWith(root);
else docGet("body").prepend(root);
const draw=spec[state.face];
const view=Object.freeze({
data:state.data,
face:state.face,
size:state.size,
theme:state.theme,
lang:state.lang,
root,
});
try{
let result=draw(view);
if(result&&typeof result.then==="function"){
let timer;
const late=new Promise((resolve)=>{
timer=setTimeout(()=>resolve(TIMEOUT),RENDER_TIMEOUT);
});
result=await Promise.race([result,late]);
clearTimeout(timer);
if(result===TIMEOUT){
if(seq===renderSeq)report("timeout","The render took over 3 seconds.");
return;
}
}
if(seq!==renderSeq)return;
if(result!==root&&!(result instanceof Node&&result.parentNode===root)){
insert(root,null,result,0);
}
if(disclosures)keepDisclosures(root,disclosures);
state.renderMS=Math.round(performance.now()-began);
}catch(error){
if(seq===renderSeq)report("script_error",describeError(error),stackPosition(error));
return;
}
await settle();
if(seq!==renderSeq)return;
post("ready",{metrics:measure(root)});
readySent=true;
if(state.face==="detail")reportHeight();
}
let lastHeight=-1;
let heightQueued=false;
let heightFrozen=false;
let growth={delta:0,since:0,count:0};
let observer=null;
function reportHeight(){
if(heightFrozen||!readySent)return;
const height=contentHeight();
if(height===lastHeight)return;
if(lastHeight>=0&&height>lastHeight){
const delta=height-lastHeight;
const now=performance.now();
if(delta===growth.delta&&now-growth.since<=1000)growth.count++;
else growth={delta,since:now,count:1};
if(growth.count>=10){
heightFrozen=true;
if(observer)observer.disconnect();
report("layout_loop",`The height kept growing by ${delta}px with the frame; stopped at ${lastHeight}px.`);
return;
}
}else growth={delta:0,since:0,count:0};
lastHeight=height;
post("resize",{height});
}
function watchHeight(){
if(typeof ResizeObserver!=="function")return;
observer=new ResizeObserver(()=>{
if(heightQueued)return;
heightQueued=true;
requestAnimationFrame(()=>{
heightQueued=false;
reportHeight();
});
});
observer.observe(docGet("documentElement"));
}
listen(window,"keydown",(event)=>{
if(state.face!=="detail"||event.defaultPrevented||event.isComposing)return;
if(event.ctrlKey||event.metaKey||event.altKey||!KEYS.has(event.key))return;
const target=event.target;
if(
target instanceof Element&&
target.closest("input, textarea, select, [contenteditable]:not([contenteditable='false'])")
){
return;
}
post("key",{key:event.key,shift:event.shiftKey});
});
function tile(definition){
if(spec)throw new Error("baum.tile() was already called: a tile registers once.");
if(!isRecord(definition)||typeof definition.glance!=="function"||typeof definition.detail!=="function"){
throw new TypeError("baum.tile() takes {glance(view), detail(view)}: two functions.");
}
spec={glance:definition.glance,detail:definition.detail};
if(started)render();
}
function jump(citation){
const{key,payload}=resolve(citation);
if(!payload||payload.status!=="ok"||payload.navigable!==true)return false;
post("jump",{key});
return true;
}
Object.defineProperty(window,"baum",{
enumerable:true,
value:Object.freeze({
tile,
html:Object.freeze(html),
cite,
diff,
md,
compare,
empty,
fail,
refs:Object.freeze({
get:(citation)=>resolve(citation).payload,
}),
jump,
ask:()=>post("ask",{}),
openDetail:()=>post("open",{}),
}),
});
defineElement("baum-cite","cite");
defineElement("baum-diff","diff");
defineElement("baum-md","md");
function hello(){
if(helloSent)return;
helloSent=true;
post("hello",{kit:KIT,registered:spec!==null});
}
if(docGet("readyState")==="loading")listen(doc,"DOMContentLoaded",hello);
else queueMicrotask(hello);
})();
