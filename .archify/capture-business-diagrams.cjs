const { chromium } = require(process.cwd()+'/studio/node_modules/@playwright/test');
(async()=>{
 const browser=await chromium.launch({channel:'chrome',headless:true});
 const root=process.cwd();
 for(const name of (process.argv.length>2 ? process.argv.slice(2) : ['business-ai-work-engineering','emberling-construction-focus-engineering'])){
  const page=await browser.newPage({viewport:{width:1920,height:1000},deviceScaleFactor:1});
  await page.setContent('<html><style>body{margin:0;background:#1c2128}svg{display:block;width:100%;height:auto}</style><body>'+require('fs').readFileSync(root+'/docs/assets/'+name+'.svg','utf8').replace(/<\?xml[^>]*>/,'')+'</body></html>');
  
  const diagramHeight = Math.ceil(await page.locator('svg').evaluate(el=>el.getBoundingClientRect().height));
  if(diagramHeight<1000) await page.setViewportSize({width:1920,height:diagramHeight});
  await page.screenshot({path:root+'/docs/assets/'+name+'.png',fullPage:true,timeout:10000});
  await page.close();
 }
 await browser.close();
})();
