import { describe, expect, it } from "vitest";
import { selectRunFreeze } from "./EvidenceFreezePanel";
import type { RunFreeze } from "./types";

const freeze=(id:string,state:string):RunFreeze=>({id,run_id:"run",profile:"paper",destination_uri:"aexp://project/evidence",state,stage:state,file_count:1,total_bytes:1,files_done:0,bytes_done:0,created_at:"2026-01-01",updated_at:"2026-01-01"});

describe("selectRunFreeze",()=>{
  it("resolves the selected id from the newest polled objects",()=>{
    const queued=freeze("freeze-1","queued");
    expect(selectRunFreeze([queued],"freeze-1")?.state).toBe("queued");
    const completed=freeze("freeze-1","released");
    expect(selectRunFreeze([completed],"freeze-1")?.state).toBe("released");
  });
});
