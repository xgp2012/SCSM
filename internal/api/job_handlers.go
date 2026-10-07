package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// This file implements the scheduler endpoints (§6.7).
//
// Jobs are stored through JobStore and executed by the scheduler. When the
// scheduler is not wired (the default here), the handlers still manage the rows
// but the listing reports scheduler_available=false, and a create/update returns
// a warning — so the UI can say "stored, not yet executed" rather than implying
// the job is live. That is deliberately different from a 501: the CRUD itself is
// real, only the execution is missing.

// handleListJobs lists scheduled jobs.
func (s *Server) handleListJobs(c *gin.Context) {
	jobs, err := s.deps.Jobs.List(c.Request.Context())
	if err != nil {
		if IsNotImplemented(err) {
			FailNotImplemented(c, "job store (§5.5 jobs)", err)
			return
		}
		Fail(c, Classify(err, "no jobs found"))
		return
	}
	if jobs == nil {
		jobs = []Job{}
	}

	c.JSON(http.StatusOK, JobListResponse{
		Data:               jobs,
		Total:              len(jobs),
		SchedulerAvailable: !s.schedulerMissing(),
	})
}

// handleCreateJob creates a scheduled job.
func (s *Server) handleCreateJob(c *gin.Context) {
	var req JobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("任务请求格式无效：%v", err))
		return
	}

	job := &Job{
		InstanceID: req.InstanceID,
		Type:       strings.TrimSpace(req.Type),
		Cron:       strings.TrimSpace(req.Cron),
		Payload:    req.Payload,
		Enabled:    req.Enabled,
	}

	if apiErr := s.validateJob(c, job); apiErr != nil {
		Fail(c, apiErr)
		return
	}

	// Verify the target instance exists, so a job cannot silently point at
	// nothing and fail at 4am.
	if job.InstanceID != 0 {
		inst, err := s.deps.Instances.GetByID(c.Request.Context(), job.InstanceID)
		if err != nil {
			Fail(c, Classify(err, fmt.Sprintf("instance %d not found", job.InstanceID)))
			return
		}
		p := MustPrincipal(c)
		if aerr := s.deps.RBAC.AuthorizeInstance(p.Role, p.UserID, inst.OwnerID, inst.ID, permInstanceConfig); aerr != nil {
			Fail(c, Forbidden("%s", aerr.Error()))
			return
		}
	}

	if err := s.deps.Jobs.Create(c.Request.Context(), job); err != nil {
		Fail(c, Classify(err, "could not create the job"))
		return
	}

	warning := s.rescheduleJob(c, job)

	s.audit(c, "job.create", fmt.Sprintf("job:%d", job.ID), gin.H{
		"type":        job.Type,
		"cron":        job.Cron,
		"instance_id": job.InstanceID,
		"enabled":     job.Enabled,
	})

	body := DataResponse{Data: job}
	if warning != "" {
		c.JSON(http.StatusCreated, gin.H{
			"data":     job,
			"warning":  warning,
			"executed": false,
		})
		return
	}
	c.JSON(http.StatusCreated, body)
}

// handleUpdateJob updates a scheduled job.
func (s *Server) handleUpdateJob(c *gin.Context) {
	id, apiErr := int64Param(c, "id", "job id")
	if apiErr != nil {
		Fail(c, apiErr)
		return
	}

	existing, err := s.deps.Jobs.GetByID(c.Request.Context(), id)
	if err != nil {
		Fail(c, Classify(err, fmt.Sprintf("job %d not found", id)))
		return
	}

	var req JobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("任务请求格式无效：%v", err))
		return
	}

	updated := &Job{
		ID:         existing.ID,
		InstanceID: req.InstanceID,
		Type:       strings.TrimSpace(req.Type),
		Cron:       strings.TrimSpace(req.Cron),
		Payload:    req.Payload,
		Enabled:    req.Enabled,
		LastRun:    existing.LastRun,
		LastResult: existing.LastResult,
	}
	if apiErr := s.validateJob(c, updated); apiErr != nil {
		Fail(c, apiErr)
		return
	}

	if err := s.deps.Jobs.Update(c.Request.Context(), updated); err != nil {
		Fail(c, Classify(err, fmt.Sprintf("job %d not found", id)))
		return
	}

	warning := s.rescheduleJob(c, updated)

	s.audit(c, "job.update", fmt.Sprintf("job:%d", id), gin.H{
		"type":        updated.Type,
		"cron":        updated.Cron,
		"enabled":     updated.Enabled,
		"instance_id": updated.InstanceID,
	})

	if warning != "" {
		c.JSON(http.StatusOK, gin.H{"data": updated, "warning": warning, "executed": false})
		return
	}
	c.JSON(http.StatusOK, DataResponse{Data: updated})
}

// handleDeleteJob deletes a scheduled job.
func (s *Server) handleDeleteJob(c *gin.Context) {
	id, apiErr := int64Param(c, "id", "job id")
	if apiErr != nil {
		Fail(c, apiErr)
		return
	}

	existing, err := s.deps.Jobs.GetByID(c.Request.Context(), id)
	if err != nil {
		Fail(c, Classify(err, fmt.Sprintf("job %d not found", id)))
		return
	}

	if err := s.deps.Jobs.Delete(c.Request.Context(), id); err != nil {
		Fail(c, Classify(err, fmt.Sprintf("job %d not found", id)))
		return
	}

	s.audit(c, "job.delete", fmt.Sprintf("job:%d", id), gin.H{
		"type":        existing.Type,
		"cron":        existing.Cron,
		"instance_id": existing.InstanceID,
	})

	c.JSON(http.StatusOK, DataResponse{Data: OKResponse{OK: true}})
}

// validateJob applies the job validation rules, delegating to JobService when it
// is available (it owns the cron parser).
func (s *Server) validateJob(c *gin.Context, job *Job) *APIError {
	if job.Type == "" {
		return ValidationFailed("必须提供任务类型")
	}
	switch job.Type {
	case "backup", "restart", "command", "announce":
	default:
		return ValidationFailed("任务类型必须是 backup、restart、command、announce 之一，实际为 %q", job.Type)
	}
	if job.Cron == "" {
		return ValidationFailed("必须提供 cron 表达式")
	}
	if len(job.Cron) > 128 {
		return ValidationFailed("cron 表达式过长")
	}

	if s.deps.Job != nil && !isNopJobService(s.deps.Job) {
		result, err := s.deps.Job.Validate(job)
		if err != nil {
			if IsNotImplemented(err) {
				return NotImplemented("任务校验（§6.7）")
			}
			return Classify(err, "invalid job")
		}
		if result != nil && hasValidationErrors(result) {
			return ValidationFailed("任务无效").WithDetail(gin.H{"issues": result.Issues})
		}
		return nil
	}

	// Minimal built-in cron sanity check so an obviously malformed expression
	// is rejected even without the scheduler: five whitespace-separated fields,
	// each of which must be in range.
	//
	// This deliberately duplicates a little of what the scheduler's parser
	// does. Without it an expression like "99 99 * * *" is accepted and stored
	// while never firing — a silent failure the operator only discovers when
	// the nightly backup they configured never ran.
	fields := strings.Fields(job.Cron)
	if len(fields) != 5 {
		return ValidationFailed("cron 表达式必须恰好包含 5 个字段（分 时 日 月 周），实际为 %d 个", len(fields)).
			WithDetail(validationDetails("cron", "invalid_cron", fmt.Errorf("expected 5 fields")))
	}
	for i, field := range fields {
		if err := validateCronField(i, field); err != nil {
			return ValidationFailed("cron 表达式无效：%v", err).
				WithDetail(validationDetails("cron", "invalid_cron", err))
		}
	}
	return nil
}

// cronFieldRange is the inclusive [min,max] for each of the five cron fields.
var cronFieldRange = [5][2]int{
	{0, 59}, // minute
	{0, 23}, // hour
	{1, 31}, // day of month
	{1, 12}, // month
	{0, 7},  // day of week (0 and 7 are both Sunday)
}

// validateCronField checks one field: "*", a list "a,b,c", a range "a-b", a
// step "*/n" or "a-b/n", with every literal in range.
func validateCronField(index int, field string) error {
	name := [...]string{"minute", "hour", "day of month", "month", "day of week"}[index]
	bounds := cronFieldRange[index]

	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return fmt.Errorf("the %s field has an empty list element", name)
		}

		// Split off a step, if present.
		value := part
		if slash := strings.IndexByte(part, '/'); slash >= 0 {
			value = part[:slash]
			step, err := strconv.Atoi(part[slash+1:])
			if err != nil || step <= 0 {
				return fmt.Errorf("the %s field has an invalid step %q", name, part[slash+1:])
			}
		}

		if value == "*" {
			continue
		}

		// A range or a single value.
		low, high := value, value
		if dash := strings.IndexByte(value, '-'); dash > 0 {
			low, high = value[:dash], value[dash+1:]
		}
		lowN, err := strconv.Atoi(low)
		if err != nil {
			return fmt.Errorf("the %s field has a non-numeric value %q", name, low)
		}
		highN, err := strconv.Atoi(high)
		if err != nil {
			return fmt.Errorf("the %s field has a non-numeric value %q", name, high)
		}
		if lowN < bounds[0] || lowN > bounds[1] || highN < bounds[0] || highN > bounds[1] {
			return fmt.Errorf("the %s field value %q is outside the valid range %d-%d",
				name, value, bounds[0], bounds[1])
		}
		if lowN > highN {
			return fmt.Errorf("the %s field range %q is inverted", name, value)
		}
	}
	return nil
}

// rescheduleJob tells the scheduler about a job change, returning a warning
// string when the scheduler is not available.
func (s *Server) rescheduleJob(_ *gin.Context, job *Job) string {
	if s.schedulerMissing() {
		return "the scheduler is not available in this build; the job is stored but will not run"
	}
	if err := s.deps.Job.Reschedule(context.Background(), job); err != nil && !IsNotImplemented(err) {
		return fmt.Sprintf("the job was saved but the scheduler could not pick it up: %v", err)
	}
	return ""
}

// schedulerMissing reports whether job execution is unavailable.
func (s *Server) schedulerMissing() bool {
	return s.deps.Job == nil || isNopJobService(s.deps.Job)
}

func isNopJobService(svc JobService) bool {
	_, ok := svc.(nopJobService)
	return ok
}
