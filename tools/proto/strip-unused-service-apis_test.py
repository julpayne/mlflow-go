#!/usr/bin/env python3
"""Tests for strip-unused-service-apis.py."""

from __future__ import annotations

import importlib.util
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name("strip-unused-service-apis.py")
SPEC = importlib.util.spec_from_file_location("strip_unused_service_apis", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
MOD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MOD)

SAMPLE = """\
syntax = "proto2";

import "issues.proto";
import "prompt_optimization.proto";
import "scalapb/scalapb.proto";

service MlflowService {
  rpc deleteAssessment(DeleteAssessment) returns (DeleteAssessment.Response) {
    option (rpc) = {
      visibility: PUBLIC_UNDOCUMENTED
    };
  }

  // Issue RPCs

  // Create an issue.
  rpc createIssue(mlflow.issues.CreateIssue) returns (mlflow.issues.CreateIssue.Response) {
    option (rpc) = {
      visibility: PUBLIC_UNDOCUMENTED
    };
  }

  // Update an existing issue.
  rpc updateIssue(mlflow.issues.UpdateIssue) returns (mlflow.issues.UpdateIssue.Response) {
    option (rpc) = {
      visibility: PUBLIC_UNDOCUMENTED
    };
  }

  // Evaluation Dataset RPCs

  // Create an evaluation dataset
  rpc createDataset(CreateDataset) returns (CreateDataset.Response) {
    option (rpc) = {
      visibility: PUBLIC_UNDOCUMENTED
    };
  }

  // Create a new prompt optimization job.
  // This endpoint initiates an optimization run with the specified configuration.
  // The optimization process runs asynchronously and can be monitored via getPromptOptimizationJob.
  rpc createPromptOptimizationJob(CreatePromptOptimizationJob) returns (CreatePromptOptimizationJob.Response) {
    option (rpc) = {
      visibility: PUBLIC
    };
  }

  // Get the details and status of a prompt optimization job.
  rpc getPromptOptimizationJob(GetPromptOptimizationJob) returns (GetPromptOptimizationJob.Response) {
    option (rpc) = {
      visibility: PUBLIC
    };
  }
}

// ========== Prompt Optimization API Messages ==========

message CreatePromptOptimizationJob {
  optional string experiment_id = 1;
  message Response {
    optional PromptOptimizationJob job = 1;
  }
}

message GetPromptOptimizationJob {
  optional string job_id = 1;
  message Response {
    optional PromptOptimizationJob job = 1;
  }
}

message PromptOptimizationJob {
  optional string job_id = 1;
}

message PromptOptimizationJobConfig {
  optional int32 max_iterations = 1;
}

message PromptOptimizationJobTag {
  optional string key = 1;
}

// =============================================================================
// Workspace Management Messages
// =============================================================================

// Workspace metadata returned by workspace APIs.
message Workspace {
  optional string name = 1;
}
"""


class StripUnusedServiceAPIsTest(unittest.TestCase):
    def test_strips_rpcs_messages_and_orphaned_comments(self) -> None:
        content = SAMPLE
        content = content.replace('import "issues.proto";\n', "")
        content = content.replace('import "prompt_optimization.proto";\n', "")
        content = MOD.remove_rpc_blocks(content)
        content = MOD.remove_messages(content)
        content = MOD.cleanup_orphans(content)

        self.assertNotIn("createIssue", content)
        self.assertNotIn("createPromptOptimizationJob", content)
        self.assertNotIn("Issue RPCs", content)
        self.assertNotIn("Create an issue", content)
        self.assertNotIn("Update an existing issue", content)
        self.assertNotIn("prompt optimization job", content)
        self.assertNotIn("Prompt Optimization API Messages", content)
        self.assertNotIn("message CreatePromptOptimizationJob", content)
        self.assertNotIn("PromptOptimizationJob", content)

        # Kept APIs and their docs / following section headers must remain.
        self.assertIn("rpc createDataset", content)
        self.assertIn("Evaluation Dataset RPCs", content)
        self.assertIn("Create an evaluation dataset", content)
        self.assertIn("Workspace Management Messages", content)
        self.assertIn("Workspace metadata returned by workspace APIs", content)
        self.assertIn("message Workspace", content)

        for token in MOD.FORBIDDEN:
            self.assertNotIn(token, content)

    def test_remove_messages_preserves_next_message_docs(self) -> None:
        # No section banner between stripped and retained messages — ordinary
        # docs belonging to Workspace must survive.
        sample = """\
// ========== Prompt Optimization API Messages ==========

message PromptOptimizationJobTag {
  optional string key = 1;
}

// Workspace metadata returned by workspace APIs.
message Workspace {
  optional string name = 1;
}
"""
        content = MOD.remove_messages(sample)
        content = MOD.cleanup_orphans(content)

        self.assertNotIn("PromptOptimizationJobTag", content)
        self.assertNotIn("Prompt Optimization API Messages", content)
        self.assertIn("Workspace metadata returned by workspace APIs", content)
        self.assertIn("message Workspace", content)

    def test_validate_proto_path_accepts_in_scope_proto(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            proto = root / "service.proto"
            proto.write_text('syntax = "proto2";\n')
            got = MOD.validate_proto_path(str(proto), allowed_dir=root)
            self.assertEqual(got, proto.resolve())

    def test_validate_proto_path_rejects_invalid_inputs(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "protos"
            root.mkdir()
            (root / "service.proto").write_text('syntax = "proto2";\n')
            outside = Path(tmp) / "escape.proto"
            outside.write_text('syntax = "proto2";\n')

            with self.assertRaisesRegex(ValueError, r"\.proto extension"):
                MOD.validate_proto_path(str(root / "notes.txt"), allowed_dir=root)
            with self.assertRaisesRegex(ValueError, r"must be under"):
                MOD.validate_proto_path(str(outside), allowed_dir=root)
            with self.assertRaisesRegex(ValueError, r"must be under"):
                MOD.validate_proto_path(str(root / ".." / "escape.proto"), allowed_dir=root)
            with self.assertRaisesRegex(ValueError, r"not found"):
                MOD.validate_proto_path(str(root / "missing.proto"), allowed_dir=root)


if __name__ == "__main__":
    unittest.main()
