<?php

/**
 * Example PHP worker for testing AMQP 1.0 driver
 */

use Spiral\Goridge;
use Spiral\RoadRunner;

ini_set('display_errors', 'stderr');
require dirname(__DIR__) . '/vendor/autoload.php';

// Create a worker
$worker = RoadRunner\Worker::create();

// Create jobs consumer
$consumer = new RoadRunner\Jobs\Consumer();

// Process jobs
while ($task = $consumer->waitTask()) {
    try {
        $name = $task->getName();
        $payload = $task->getPayload();
        $headers = $task->getHeaders();
        
        echo sprintf(
            "[%s] Processing job: %s with payload: %s\n", 
            date('Y-m-d H:i:s'), 
            $name, 
            $payload
        );
        
        // Simulate job processing based on job name
        switch ($name) {
            case 'email.send':
                processEmail($payload, $headers);
                break;
                
            case 'file.process':
                processFile($payload, $headers);
                break;
                
            case 'notification.send':
                processNotification($payload, $headers);
                break;
                
            default:
                echo "Unknown job type: $name\n";
                break;
        }
        
        // Acknowledge successful processing
        $task->ack();
        
    } catch (\Throwable $e) {
        echo sprintf(
            "[%s] Error processing job: %s\n", 
            date('Y-m-d H:i:s'), 
            $e->getMessage()
        );
        
        // Reject the task (will requeue based on configuration)
        $task->nack();
    }
}

function processEmail(string $payload, array $headers): void
{
    $data = json_decode($payload, true);
    
    echo sprintf(
        "Sending email to: %s, subject: %s\n",
        $data['to'] ?? 'unknown',
        $data['subject'] ?? 'no subject'
    );
    
    // Simulate email sending delay
    usleep(100000); // 100ms
}

function processFile(string $payload, array $headers): void
{
    $data = json_decode($payload, true);
    
    echo sprintf(
        "Processing file: %s, type: %s\n",
        $data['filename'] ?? 'unknown',
        $data['type'] ?? 'unknown'
    );
    
    // Simulate file processing delay
    usleep(500000); // 500ms
}

function processNotification(string $payload, array $headers): void
{
    $data = json_decode($payload, true);
    
    echo sprintf(
        "Sending notification: %s to user: %s\n",
        $data['message'] ?? 'no message',
        $data['user_id'] ?? 'unknown'
    );
    
    // Simulate notification sending delay
    usleep(50000); // 50ms
}
